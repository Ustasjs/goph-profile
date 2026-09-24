package metrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// storageScrapeTimeout caps the database query a scrape triggers, so
// a slow database delays metrics instead of hanging them.
const storageScrapeTimeout = 2 * time.Second

var (
	descPoolTotal = prometheus.NewDesc("pgxpool_total_conns",
		"Total connections in the pgx pool.", nil, nil)
	descPoolIdle = prometheus.NewDesc("pgxpool_idle_conns",
		"Idle connections in the pgx pool.", nil, nil)
	descPoolAcquired = prometheus.NewDesc("pgxpool_acquired_conns",
		"Connections currently acquired from the pgx pool.", nil, nil)

	descStorage = prometheus.NewDesc("avatars_storage_bytes",
		"Live avatar bytes per user. The user_id label is fine for a "+
			"training stand; unbounded users would need an aggregate.",
		[]string{"user_id"}, nil)
)

// poolCollector reads pgxpool.Stat on every scrape.
type poolCollector struct {
	stat func() *pgxpool.Stat
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descPoolTotal
	ch <- descPoolIdle
	ch <- descPoolAcquired
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()
	ch <- prometheus.MustNewConstMetric(descPoolTotal, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(descPoolIdle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(descPoolAcquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
}

// storageCollector queries the storage usage on every scrape: always
// accurate, survives restarts, and the table is small enough for a
// grouped sum to stay cheap.
type storageCollector struct {
	usage func(ctx context.Context) (map[string]int64, error)
}

func (c *storageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descStorage
}

func (c *storageCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), storageScrapeTimeout)
	defer cancel()

	byUser, err := c.usage(ctx)
	if err != nil {
		// No value beats a wrong value: the gap on the graph is the
		// signal that the database was unreachable.
		return
	}
	for userID, bytes := range byUser {
		ch <- prometheus.MustNewConstMetric(descStorage, prometheus.GaugeValue, float64(bytes), userID)
	}
}
