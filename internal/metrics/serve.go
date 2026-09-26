package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// Serve exposes the handler on addr under /metrics until the context
// is canceled. The worker has no HTTP server of its own, so this is
// its scrape endpoint.
func Serve(ctx context.Context, addr string, h http.Handler) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return serve(ctx, ln, h)
}

// serve is split from Serve so tests can bring their own listener
// and know the address without polling.
func serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", h)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
