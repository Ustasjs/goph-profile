package httpserver

import (
	"context"
	"net/http"
	"time"
)

const healthTimeout = 5 * time.Second

// HealthCheck probes one dependency for /health.
type HealthCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// healthHandler answers 200 when every component is up and 503
// otherwise, always with the per-component detail.
func healthHandler(checks []HealthCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()

		components := make(map[string]string, len(checks))
		status := "ok"
		for _, c := range checks {
			if err := c.Check(ctx); err != nil {
				components[c.Name] = err.Error()
				status = "degraded"
				continue
			}
			components[c.Name] = "ok"
		}

		code := http.StatusOK
		if status != "ok" {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"status":     status,
			"components": components,
		})
	}
}
