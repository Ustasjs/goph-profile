package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/ustasjs/goph-profile/internal/avatar"
	"github.com/ustasjs/goph-profile/internal/server/service"
)

// fakeService returns canned answers and records calls.
type fakeService struct {
	uploaded  avatar.Avatar
	uploadErr error
	stored    avatar.Avatar
	getErr    error
	list      []avatar.Avatar
	listErr   error
	deleteErr error
	panics    bool

	gotUserID   string
	gotFileName string
	gotData     []byte
}

func (f *fakeService) Upload(_ context.Context, userID, fileName string, data []byte) (avatar.Avatar, error) {
	f.gotUserID, f.gotFileName, f.gotData = userID, fileName, data
	return f.uploaded, f.uploadErr
}

func (f *fakeService) Metadata(context.Context, string) (avatar.Avatar, error) {
	if f.panics {
		panic("metadata blew up")
	}
	return f.stored, f.getErr
}

func (f *fakeService) List(context.Context, string) ([]avatar.Avatar, error) {
	return f.list, f.listErr
}

func (f *fakeService) GetFile(context.Context, string) (service.File, error) {
	return f.file()
}

func (f *fakeService) LatestFile(context.Context, string) (service.File, error) {
	return f.file()
}

func (f *fakeService) ThumbnailFile(context.Context, string, string) (service.File, error) {
	return f.file()
}

func (f *fakeService) file() (service.File, error) {
	if f.getErr != nil {
		return service.File{}, f.getErr
	}
	return service.File{
		Body:        io.NopCloser(strings.NewReader("image bytes")),
		ContentType: "image/png",
		Size:        11,
	}, nil
}

func (f *fakeService) Delete(_ context.Context, _, userID string) error {
	f.gotUserID = userID
	return f.deleteErr
}

func (f *fakeService) DeleteLatest(_ context.Context, _, userID string) error {
	f.gotUserID = userID
	return f.deleteErr
}

func newTestServer(t *testing.T, svc AvatarService, checks ...HealthCheck) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewRouter(svc, checks, zap.NewNop()))
	t.Cleanup(srv.Close)
	return srv
}

// testResp is a fully read response: the body is already closed, so
// the tests stay free of bodyclose ceremony.
type testResp struct {
	status int
	header http.Header
	body   []byte
}

func (r testResp) json(t *testing.T, into any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.body, into))
}

func doReq(t *testing.T, srv *httptest.Server, method, path, userID, contentType string, body io.Reader) testResp {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, body)
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return testResp{status: resp.StatusCode, header: resp.Header, body: data}
}

// multipartBody builds a multipart form with one file field.
func multipartBody(t *testing.T, field string, payload []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, "pic.png")
	require.NoError(t, err)
	_, err = fw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

func doUpload(t *testing.T, srv *httptest.Server, path, field, userID string, payload []byte) testResp {
	t.Helper()
	body, contentType := multipartBody(t, field, payload)
	return doReq(t, srv, http.MethodPost, path, userID, contentType, body)
}

func doGet(t *testing.T, srv *httptest.Server, path string) testResp {
	t.Helper()
	return doReq(t, srv, http.MethodGet, path, "", "", nil)
}

func doDelete(t *testing.T, srv *httptest.Server, path, userID string) testResp {
	t.Helper()
	return doReq(t, srv, http.MethodDelete, path, userID, "", nil)
}

func TestUpload(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	svc := &fakeService{uploaded: avatar.Avatar{ID: "id-1", UserID: "u1", CreatedAt: now}}
	srv := newTestServer(t, svc)

	for _, field := range []string{"file", "image"} {
		t.Run(field, func(t *testing.T) {
			resp := doUpload(t, srv, "/api/v1/avatars", field, "u1", []byte("data"))
			require.Equal(t, http.StatusCreated, resp.status)

			var got map[string]any
			resp.json(t, &got)
			assert.Equal(t, "id-1", got["id"])
			assert.Equal(t, "u1", got["user_id"])
			assert.Equal(t, "/api/v1/avatars/id-1", got["url"])
			assert.Equal(t, "processing", got["status"])

			assert.Equal(t, "u1", svc.gotUserID)
			assert.Equal(t, "pic.png", svc.gotFileName)
			assert.Equal(t, []byte("data"), svc.gotData)
		})
	}
}

func TestUploadAliasWebRoute(t *testing.T) {
	svc := &fakeService{uploaded: avatar.Avatar{ID: "id-1", UserID: "u1"}}
	srv := newTestServer(t, svc)

	resp := doUpload(t, srv, "/web/upload", "image", "u1", []byte("data"))
	assert.Equal(t, http.StatusCreated, resp.status)
}

func TestUploadWithoutUser(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doUpload(t, srv, "/api/v1/avatars", "file", "", []byte("data"))
	assert.Equal(t, http.StatusBadRequest, resp.status)
}

func TestUserIDTooLong(t *testing.T) {
	srv := newTestServer(t, &fakeService{})
	longID := strings.Repeat("x", maxUserIDLen+1)

	resp := doUpload(t, srv, "/api/v1/avatars", "file", longID, []byte("data"))
	assert.Equal(t, http.StatusBadRequest, resp.status)

	resp = doDelete(t, srv, "/api/v1/avatars/id-1", longID)
	assert.Equal(t, http.StatusBadRequest, resp.status)

	// Exactly at the limit is still fine.
	resp = doUpload(t, srv, "/api/v1/avatars", "file", strings.Repeat("x", maxUserIDLen), []byte("data"))
	assert.Equal(t, http.StatusCreated, resp.status)
}

func TestUploadWithoutFileField(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doUpload(t, srv, "/api/v1/avatars", "wrong", "u1", []byte("data"))
	assert.Equal(t, http.StatusBadRequest, resp.status)
}

func TestUploadTooLarge(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doUpload(t, srv, "/api/v1/avatars", "file", "u1", bytes.Repeat([]byte("x"), maxUploadBytes+1))
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.status)

	var got map[string]any
	resp.json(t, &got)
	assert.Equal(t, "File too large", got["error"])
	assert.Equal(t, float64(maxUploadBytes), got["max_size"])
}

func TestGetFile(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doGet(t, srv, "/api/v1/avatars/id-1")
	require.Equal(t, http.StatusOK, resp.status)
	assert.Equal(t, "image/png", resp.header.Get("Content-Type"))
	assert.Equal(t, "11", resp.header.Get("Content-Length"))
	assert.Equal(t, "image bytes", string(resp.body))
}

func TestGetFileNotFound(t *testing.T) {
	srv := newTestServer(t, &fakeService{getErr: avatar.ErrNotFound})

	resp := doGet(t, srv, "/api/v1/avatars/nope")
	assert.Equal(t, http.StatusNotFound, resp.status)
}

func TestLatestFile(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doGet(t, srv, "/api/v1/users/u1/avatar")
	require.Equal(t, http.StatusOK, resp.status)
	assert.Equal(t, "image bytes", string(resp.body))
}

func TestThumbnail(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doGet(t, srv, "/api/v1/avatars/id-1/thumbnails/100x100")
	require.Equal(t, http.StatusOK, resp.status)
	assert.Equal(t, "image bytes", string(resp.body))
}

func TestMetadata(t *testing.T) {
	svc := &fakeService{stored: avatar.Avatar{
		ID: "id-1", UserID: "u1", FileName: "pic.png", MimeType: "image/png",
		SizeBytes: 1024, Width: 640, Height: 480,
		Thumbnails: map[string]string{
			"300x300": "thumbnails/id-1/300x300.jpg",
			"100x100": "thumbnails/id-1/100x100.jpg",
		},
		ProcessingStatus: avatar.ProcessingStatusCompleted,
	}}
	srv := newTestServer(t, svc)

	resp := doGet(t, srv, "/api/v1/avatars/id-1/metadata")
	require.Equal(t, http.StatusOK, resp.status)

	var got metadataResponse
	resp.json(t, &got)
	assert.Equal(t, "id-1", got.ID)
	assert.Equal(t, int64(1024), got.Size)
	assert.Equal(t, dimensions{Width: 640, Height: 480}, got.Dimensions)
	// Sorted by size name, urls resolvable.
	require.Len(t, got.Thumbnails, 2)
	assert.Equal(t, thumbnail{Size: "100x100", URL: "/api/v1/avatars/id-1/thumbnails/100x100"}, got.Thumbnails[0])
	assert.Equal(t, thumbnail{Size: "300x300", URL: "/api/v1/avatars/id-1/thumbnails/300x300"}, got.Thumbnails[1])
}

func TestList(t *testing.T) {
	svc := &fakeService{list: []avatar.Avatar{{ID: "a"}, {ID: "b"}}}
	srv := newTestServer(t, svc)

	resp := doGet(t, srv, "/api/v1/users/u1/avatars")
	require.Equal(t, http.StatusOK, resp.status)

	var got []metadataResponse
	resp.json(t, &got)
	assert.Len(t, got, 2)
}

func TestListEmptyIsJSONArray(t *testing.T) {
	svc := &fakeService{list: []avatar.Avatar{}}
	srv := newTestServer(t, svc)

	resp := doGet(t, srv, "/api/v1/users/u1/avatars")
	assert.Equal(t, "[]", strings.TrimSpace(string(resp.body)))
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		err    error
		want   int
	}{
		{"ok", "u1", nil, http.StatusNoContent},
		{"no header", "", nil, http.StatusBadRequest},
		{"not owner", "u2", avatar.ErrNotOwner, http.StatusForbidden},
		{"missing", "u1", avatar.ErrNotFound, http.StatusNotFound},
		{"internal", "u1", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &fakeService{deleteErr: tt.err})
			resp := doDelete(t, srv, "/api/v1/avatars/id-1", tt.userID)
			assert.Equal(t, tt.want, resp.status)
		})
	}
}

func TestDeleteLatest(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doDelete(t, srv, "/api/v1/users/u1/avatar", "u1")
	assert.Equal(t, http.StatusNoContent, resp.status)
}

func TestHealth(t *testing.T) {
	ok := HealthCheck{Name: "db", Check: func(context.Context) error { return nil }}
	bad := HealthCheck{Name: "s3", Check: func(context.Context) error { return errors.New("down") }}

	t.Run("all ok", func(t *testing.T) {
		srv := newTestServer(t, &fakeService{}, ok)

		resp := doGet(t, srv, "/health")
		assert.Equal(t, http.StatusOK, resp.status)
	})

	t.Run("degraded", func(t *testing.T) {
		srv := newTestServer(t, &fakeService{}, ok, bad)

		resp := doGet(t, srv, "/health")
		require.Equal(t, http.StatusServiceUnavailable, resp.status)

		var got struct {
			Status     string            `json:"status"`
			Components map[string]string `json:"components"`
		}
		resp.json(t, &got)
		assert.Equal(t, "degraded", got.Status)
		assert.Equal(t, "ok", got.Components["db"])
		assert.Equal(t, "down", got.Components["s3"])
	})
}

func TestServiceErrorsMapTo500(t *testing.T) {
	boom := errors.New("boom")
	svc := &fakeService{uploadErr: boom, getErr: boom, listErr: boom}
	srv := newTestServer(t, svc)

	resp := doUpload(t, srv, "/api/v1/avatars", "file", "u1", []byte("data"))
	assert.Equal(t, http.StatusInternalServerError, resp.status)

	for _, path := range []string{
		"/api/v1/avatars/id-1",
		"/api/v1/avatars/id-1/thumbnails/100x100",
		"/api/v1/avatars/id-1/metadata",
		"/api/v1/users/u1/avatar",
		"/api/v1/users/u1/avatars",
	} {
		t.Run(path, func(t *testing.T) {
			resp := doGet(t, srv, path)
			assert.Equal(t, http.StatusInternalServerError, resp.status)
		})
	}
}

func TestDeleteLatestWithoutHeader(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	resp := doDelete(t, srv, "/api/v1/users/u1/avatar", "")
	assert.Equal(t, http.StatusBadRequest, resp.status)
}

func TestPanicRecovery(t *testing.T) {
	srv := newTestServer(t, &fakeService{panics: true})

	// The panic must become a 500, not a dropped connection.
	resp := doGet(t, srv, "/api/v1/avatars/id-1/metadata")
	assert.Equal(t, http.StatusInternalServerError, resp.status)
}

func TestServerShutdown(t *testing.T) {
	srv := New("127.0.0.1:0", &fakeService{}, nil, zap.NewNop())

	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	// Let the listener come up before shutting it down.
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
	assert.ErrorIs(t, <-done, http.ErrServerClosed)
}

func TestWebPages(t *testing.T) {
	srv := newTestServer(t, &fakeService{})

	for _, path := range []string{"/", "/web/upload", "/web/gallery/u1"} {
		t.Run(path, func(t *testing.T) {
			resp := doGet(t, srv, path)
			assert.Equal(t, http.StatusOK, resp.status)
			assert.Contains(t, resp.header.Get("Content-Type"), "text/html")
			assert.Contains(t, string(resp.body), "<html")
		})
	}
}
