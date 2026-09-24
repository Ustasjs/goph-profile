// Package httpserver exposes the avatar service over REST and
// serves the web UI.
package httpserver

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/ustasjs/goph-profile/internal/metrics"
)

const (
	readHeaderTimeout = 5 * time.Second
	// Streaming uploads and downloads set no absolute write timeout;
	// the header timeout still protects against idle clients.
)

// Server is the HTTP server with graceful shutdown.
type Server struct {
	http *http.Server

	mu   sync.Mutex
	addr net.Addr
}

// New builds the server with all routes attached.
func New(addr string, svc AvatarService, checks []HealthCheck, m *metrics.Server, log *slog.Logger) *Server {
	return &Server{
		http: &http.Server{
			Addr:              addr,
			Handler:           NewRouter(svc, checks, m, log),
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}
}

// ListenAndServe blocks until Shutdown or a listener error.
func (s *Server) ListenAndServe() error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", s.http.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.addr = ln.Addr()
	s.mu.Unlock()

	return s.http.Serve(ln)
}

// Addr returns the bound listen address, or nil until the listener
// is up. With a configured ":0" this is the only way to learn the
// real port, and readiness checks poll it instead of guessing time.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Shutdown stops accepting connections and waits for the active
// requests within the context deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// NewRouter wires the routes. Split from New so tests can drive the
// handlers through httptest without opening a port.
func NewRouter(svc AvatarService, checks []HealthCheck, m *metrics.Server, log *slog.Logger) http.Handler {
	h := &handlers{svc: svc, m: m, log: log}

	r := chi.NewRouter()
	r.Use(recovery(log), tracing(), m.Middleware(), logging(log))

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/avatars", h.upload)
		r.Get("/avatars/{avatarID}", h.getFile)
		r.Get("/avatars/{avatarID}/thumbnails/{size}", h.getThumbnail)
		r.Get("/avatars/{avatarID}/metadata", h.metadata)
		r.Delete("/avatars/{avatarID}", h.deleteAvatar)
		r.Get("/users/{userID}/avatar", h.latestFile)
		r.Delete("/users/{userID}/avatar", h.deleteLatest)
		r.Get("/users/{userID}/avatars", h.list)
	})

	r.Get("/health", healthHandler(checks))
	r.Method(http.MethodGet, "/metrics", m.Handler())

	r.Get("/", servePage(pageUpload))
	r.Get("/web/upload", servePage(pageUpload))
	// The spec lists a form-processing route; the SPA posts to the
	// API directly, so this is an alias of the same handler.
	r.Post("/web/upload", h.upload)
	r.Get("/web/gallery/{userID}", servePage(pageGallery))

	// otelhttp opens the server span; health checks and metric
	// scrapes fire every few seconds and would drown real requests
	// in the trace UI, so they are not traced.
	return otelhttp.NewHandler(r, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/health" && r.URL.Path != "/metrics"
		}))
}

// tracing names the server span after the matched route and exposes
// the trace id to the client, so any curl response can be looked up
// in the trace UI directly.
func tracing() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			span := trace.SpanFromContext(r.Context())
			if sc := span.SpanContext(); sc.IsValid() {
				w.Header().Set("X-Trace-Id", sc.TraceID().String())
			}
			next.ServeHTTP(w, r)
			// The route pattern is known only after routing, hence
			// the rename after the handler instead of a span name at
			// the start.
			if pattern := chi.RouteContext(r.Context()).RoutePattern(); pattern != "" {
				span.SetName(r.Method + " " + pattern)
			}
		})
	}
}

// logging writes one line per request.
func logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.InfoContext(r.Context(), "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration", time.Since(start))
		})
	}
}

// recovery turns a handler panic into a 500 instead of killing the
// connection.
func recovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.ErrorContext(r.Context(), "handler panic", "panic", rec, "path", r.URL.Path)
					writeError(w, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
