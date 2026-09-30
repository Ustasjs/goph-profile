// Package metrics defines the Prometheus instruments of the server
// and the worker. Each binary owns a registry, so a unit test can
// build as many instances as it needs without duplicate-registration
// panics.
package metrics

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Upload outcome labels. Rejected covers client mistakes (no user id,
// oversized or unreadable request): they are not service failures and
// must not look like ones on a graph.
const (
	StatusOK       = "ok"
	StatusError    = "error"
	StatusRejected = "rejected"
	StatusSkipped  = "skipped"
)

// durationBuckets covers the 100ms..30s range of file operations:
// the default buckets crowd around milliseconds and would flatten
// every percentile of an upload into the last bucket.
var durationBuckets = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// base carries what both binaries share: the registry, its HTTP
// handler and the infrastructure collectors.
type base struct {
	reg *prometheus.Registry
}

func newBase() base {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return base{reg: reg}
}

// Handler serves the registry in the Prometheus text format.
func (b base) Handler() http.Handler {
	return promhttp.HandlerFor(b.reg, promhttp.HandlerOpts{})
}

// RegisterPool exposes the pgxpool connection gauges.
func (b base) RegisterPool(stat func() *pgxpool.Stat) {
	b.reg.MustRegister(&poolCollector{stat: stat})
}

// Server holds the HTTP-facing instruments.
type Server struct {
	base

	requests       *prometheus.CounterVec
	duration       *prometheus.HistogramVec
	uploads        *prometheus.CounterVec
	uploadDuration *prometheus.HistogramVec
}

// NewServer builds the server registry with all instruments.
func NewServer() *Server {
	b := newBase()
	f := promauto.With(b.reg)
	return &Server{
		base: b,
		requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		duration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		uploads: f.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_uploads_total",
			Help: "Avatar uploads: ok, error (service failure) or rejected (client mistake).",
		}, []string{"status"}),
		uploadDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "avatars_upload_duration_seconds",
			Help:    "Avatar upload duration by outcome.",
			Buckets: durationBuckets,
		}, []string{"status"}),
	}
}

// ObserveUpload records one upload outcome.
func (s *Server) ObserveUpload(status string, seconds float64) {
	s.uploads.WithLabelValues(status).Inc()
	s.uploadDuration.WithLabelValues(status).Observe(seconds)
}

// RegisterStorage exposes the per-user storage usage. The reader runs
// on every scrape, so it must stay cheap; a failed read publishes
// nothing rather than a stale or partial value.
func (s *Server) RegisterStorage(usage func(ctx context.Context) (map[string]int64, error)) {
	s.reg.MustRegister(&storageCollector{usage: usage})
}

// ObserveRequest records one finished HTTP request in the RED
// instruments. Capturing the status and matching the route is the
// HTTP layer's job; this package only turns them into label values.
func (s *Server) ObserveRequest(method, route string, status int, seconds float64) {
	s.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	s.duration.WithLabelValues(method, route).Observe(seconds)
}

// Worker holds the event-processing instruments.
type Worker struct {
	base

	processed    *prometheus.CounterVec
	procDuration *prometheus.HistogramVec
}

// NewWorker builds the worker registry with all instruments.
func NewWorker() *Worker {
	b := newBase()
	f := promauto.With(b.reg)
	return &Worker{
		base: b,
		processed: f.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_processed_total",
			Help: "Processing attempts by event and outcome: ok, error or skipped (repeated delivery). Retries count as separate attempts.",
		}, []string{"event", "status"}),
		procDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "avatars_processing_duration_seconds",
			Help:    "Event processing duration by event type.",
			Buckets: durationBuckets,
		}, []string{"event"}),
	}
}

// ObserveProcessed records one processing attempt.
func (w *Worker) ObserveProcessed(event, status string, seconds float64) {
	w.processed.WithLabelValues(event, status).Inc()
	w.procDuration.WithLabelValues(event).Observe(seconds)
}
