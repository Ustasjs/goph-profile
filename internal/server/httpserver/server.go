// Package httpserver exposes the avatar service over REST and
// serves the web UI.
package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	readHeaderTimeout = 5 * time.Second
	// Streaming uploads and downloads set no absolute write timeout;
	// the header timeout still protects against idle clients.
)

// Server is the HTTP server with graceful shutdown.
type Server struct {
	http *http.Server
}

// New builds the server with all routes attached.
func New(addr string, svc AvatarService, checks []HealthCheck, log *zap.Logger) *Server {
	return &Server{
		http: &http.Server{
			Addr:              addr,
			Handler:           NewRouter(svc, checks, log),
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}
}

// ListenAndServe blocks until Shutdown or a listener error.
func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

// Shutdown stops accepting connections and waits for the active
// requests within the context deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// NewRouter wires the routes. Split from New so tests can drive the
// handlers through httptest without opening a port.
func NewRouter(svc AvatarService, checks []HealthCheck, log *zap.Logger) http.Handler {
	h := &handlers{svc: svc, log: log}

	r := chi.NewRouter()
	r.Use(recovery(log), logging(log))

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

	r.Get("/", servePage(pageUpload))
	r.Get("/web/upload", servePage(pageUpload))
	// The spec lists a form-processing route; the SPA posts to the
	// API directly, so this is an alias of the same handler.
	r.Post("/web/upload", h.upload)
	r.Get("/web/gallery/{userID}", servePage(pageGallery))

	return r
}

// logging writes one line per request.
func logging(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", sw.status),
				zap.Duration("duration", time.Since(start)))
		})
	}
}

// recovery turns a handler panic into a 500 instead of killing the
// connection.
func recovery(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("handler panic", zap.Any("panic", rec), zap.String("path", r.URL.Path))
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
