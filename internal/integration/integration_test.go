// Package integration_test checks the whole system: the HTTP API
// stores files in a real MinIO and metadata in a real Postgres, the
// events travel through a real RabbitMQ, and the worker's processor
// builds real thumbnails.
//
// Other tests cover the parts: handlers run against a fake service,
// the service against fake stores, and each store against its own
// backend. Only here everything meets, so the seams are checked:
// the event payloads between server and worker, the thumbnail keys
// between the worker and the metadata answers, and the delete
// cleanup between the API and the bucket.
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/ustasjs/goph-profile/internal/avatar"
	"github.com/ustasjs/goph-profile/internal/broker"
	"github.com/ustasjs/goph-profile/internal/metrics"
	"github.com/ustasjs/goph-profile/internal/server/httpserver"
	"github.com/ustasjs/goph-profile/internal/server/service"
	"github.com/ustasjs/goph-profile/internal/storage/postgres"
	"github.com/ustasjs/goph-profile/internal/storage/s3"
	"github.com/ustasjs/goph-profile/internal/worker/processor"
	"github.com/ustasjs/goph-profile/migrations"
)

// system is the whole application wired to real backends.
type system struct {
	api   *httptest.Server
	files *s3.Store
}

// startSystem builds the server and the worker on real Postgres,
// MinIO and RabbitMQ. Without the environment variables the test is
// skipped, so plain "go test" works with nothing around.
func startSystem(t *testing.T) *system {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	s3Endpoint := os.Getenv("S3_ENDPOINT")
	rabbitURL := os.Getenv("RABBITMQ_URL")
	if dsn == "" || s3Endpoint == "" || rabbitURL == "" {
		t.Skip("DATABASE_DSN, S3_ENDPOINT or RABBITMQ_URL is not set")
	}

	log := zap.NewNop()
	require.NoError(t, migrations.Run(dsn))

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	files, err := s3.New(s3.Config{
		Endpoint:  s3Endpoint,
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		Bucket:    "e2e-avatars",
	})
	require.NoError(t, err)
	require.NoError(t, files.EnsureBucket(context.Background()))

	pub, err := broker.NewPublisher(rabbitURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close() })

	repo := postgres.New(pool)
	svc := service.New(repo, files, pub, log)
	api := httptest.NewServer(httpserver.NewRouter(svc, nil, metrics.NewServer(), log))
	t.Cleanup(api.Close)

	// The worker side: a real consumer drives the real processor.
	cons, err := broker.NewConsumer(rabbitURL, 1, log)
	require.NoError(t, err)
	cons.SetBackoff([]time.Duration{100 * time.Millisecond})

	proc := processor.New(repo, files, metrics.NewWorker(), log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- cons.Run(ctx, broker.Handlers{
			OnUpload:     proc.HandleUpload,
			OnDelete:     proc.HandleDelete,
			OnUploadDead: proc.UploadFailed,
		})
	}()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	return &system{api: api, files: files}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

func upload(t *testing.T, sys *system, userID string, payload []byte) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "e2e.png")
	require.NoError(t, err)
	_, err = fw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, sys.api.URL+"/api/v1/avatars", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-User-ID", userID)

	resp, err := sys.api.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	return created
}

func get(t *testing.T, sys *system, path string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, sys.api.URL+path, nil)
	require.NoError(t, err)
	resp, err := sys.api.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, body
}

// metadataOf polls the metadata endpoint until cond holds, so the
// test survives the asynchronous processing delay.
func metadataOf(t *testing.T, sys *system, id string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	var last map[string]any
	require.Eventually(t, func() bool {
		status, _, body := get(t, sys, "/api/v1/avatars/"+id+"/metadata")
		if status != http.StatusOK {
			return false
		}
		var meta map[string]any
		if err := json.Unmarshal(body, &meta); err != nil {
			return false
		}
		last = meta
		return cond(meta)
	}, 15*time.Second, 200*time.Millisecond, "metadata never reached the expected state: %v", &last)
	return last
}

func TestUploadProcessDelete(t *testing.T) {
	sys := startSystem(t)
	userID := "e2e-user"

	// Upload: the API answers 201 with the public state "processing".
	created := upload(t, sys, userID, pngBytes(t, 640, 480))
	id := created["id"].(string)
	assert.Equal(t, "processing", created["status"])

	// The worker picks the event up and the thumbnails appear.
	meta := metadataOf(t, sys, id, func(m map[string]any) bool {
		return m["status"] == avatar.ProcessingStatusCompleted
	})
	thumbs := meta["thumbnails"].([]any)
	require.Len(t, thumbs, 2)

	// Every advertised thumbnail URL streams a decodable JPEG of the
	// advertised size.
	for _, raw := range thumbs {
		thumb := raw.(map[string]any)
		status, header, body := get(t, sys, thumb["url"].(string))
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "image/jpeg", header.Get("Content-Type"))

		img, format, err := image.Decode(bytes.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, "jpeg", format)
		wantPx := map[string]int{"100x100": 100, "300x300": 300}[thumb["size"].(string)]
		assert.Equal(t, wantPx, img.Bounds().Dx())
	}

	// The gallery listing sees the avatar.
	status, _, body := get(t, sys, "/api/v1/users/"+userID+"/avatars")
	require.Equal(t, http.StatusOK, status)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(body, &list))
	require.NotEmpty(t, list)

	// Delete: the API hides the avatar at once...
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodDelete, sys.api.URL+"/api/v1/avatars/"+id, nil)
	require.NoError(t, err)
	req.Header.Set("X-User-ID", userID)
	resp, err := sys.api.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	getStatus, _, _ := get(t, sys, "/api/v1/avatars/"+id)
	assert.Equal(t, http.StatusNotFound, getStatus)

	// ...and the worker removes the objects from the bucket.
	require.Eventually(t, func() bool {
		_, err := sys.files.Get(context.Background(), avatar.OriginalKey(id))
		if err == nil {
			return false
		}
		for _, px := range avatar.ThumbnailSizes {
			if _, err := sys.files.Get(context.Background(), avatar.ThumbnailKey(id, px)); err == nil {
				return false
			}
		}
		return true
	}, 15*time.Second, 200*time.Millisecond)
}

func TestNonImageEndsFailed(t *testing.T) {
	sys := startSystem(t)

	created := upload(t, sys, "e2e-user", []byte("plain text, not an image"))
	id := created["id"].(string)

	// The worker cannot decode it; the avatar must settle as failed,
	// not hang in pending forever.
	metadataOf(t, sys, id, func(m map[string]any) bool {
		return m["status"] == avatar.ProcessingStatusFailed
	})
}
