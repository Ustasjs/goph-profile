package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doGet performs one request and discards the response.
func doGet(t *testing.T, srv *httptest.Server, path string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func TestMiddlewareRecordsRoutePattern(t *testing.T) {
	m := NewServer()

	r := chi.NewRouter()
	r.Use(m.Middleware())
	r.Get("/api/v1/avatars/{avatarID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	doGet(t, srv, "/api/v1/avatars/abc")

	got := testutil.ToFloat64(m.requests.WithLabelValues("GET", "/api/v1/avatars/{avatarID}", "404"))
	assert.Equal(t, 1.0, got)
	assert.Equal(t, 1, testutil.CollectAndCount(m.duration, "http_request_duration_seconds"))
}

func TestMiddlewareSkipsHealthAndMetrics(t *testing.T) {
	m := NewServer()

	r := chi.NewRouter()
	r.Use(m.Middleware())
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	r.Get("/health", ok)
	r.Get("/metrics", ok)

	srv := httptest.NewServer(r)
	defer srv.Close()
	for _, path := range []string{"/health", "/metrics"} {
		doGet(t, srv, path)
	}

	assert.Equal(t, 0, testutil.CollectAndCount(m.requests, "http_requests_total"))
}

func TestObserveUpload(t *testing.T) {
	m := NewServer()
	m.ObserveUpload(StatusOK, 1.5)
	m.ObserveUpload(StatusRejected, 0.01)

	assert.Equal(t, 1.0, testutil.ToFloat64(m.uploads.WithLabelValues(StatusOK)))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.uploads.WithLabelValues(StatusRejected)))
	assert.Equal(t, 0.0, testutil.ToFloat64(m.uploads.WithLabelValues(StatusError)))
}

func TestObserveProcessed(t *testing.T) {
	m := NewWorker()
	m.ObserveProcessed("upload", StatusOK, 0.5)
	m.ObserveProcessed("upload", StatusSkipped, 0.001)
	m.ObserveProcessed("delete", StatusError, 0.1)

	assert.Equal(t, 1.0, testutil.ToFloat64(m.processed.WithLabelValues("upload", StatusOK)))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.processed.WithLabelValues("upload", StatusSkipped)))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.processed.WithLabelValues("delete", StatusError)))
}

func TestStorageCollector(t *testing.T) {
	m := NewServer()
	m.RegisterStorage(func(context.Context) (map[string]int64, error) {
		return map[string]int64{"u1": 1024}, nil
	})

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	assert.Contains(t, body, `avatars_storage_bytes{user_id="u1"} 1024`)
}

func TestStorageCollectorSkipsOnError(t *testing.T) {
	m := NewServer()
	m.RegisterStorage(func(context.Context) (map[string]int64, error) {
		return nil, errors.New("db down")
	})

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "avatars_storage_bytes{")
}

func TestServeShutsDownOnCancel(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, NewWorker().Handler()) }()

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, "http://"+ln.Addr().String()+"/metrics", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
}

func TestHandlerServesGoCollector(t *testing.T) {
	rec := httptest.NewRecorder()
	NewWorker().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.True(t, strings.Contains(rec.Body.String(), "go_goroutines"))
}
