package observability

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// pgxPoolCollector exports pgxpool.Stat() gauges/counters to Prometheus on each
// scrape. EmptyAcquireCount is the canary for pool saturation: a rising value
// means goroutines are waiting on connections.
type pgxPoolCollector struct {
	pool *pgxpool.Pool
	name string

	totalConns              *prometheus.Desc
	idleConns               *prometheus.Desc
	acquiredConns           *prometheus.Desc
	constructingConns       *prometheus.Desc
	maxConns                *prometheus.Desc
	acquireCount            *prometheus.Desc
	emptyAcquireCount       *prometheus.Desc
	canceledAcquires        *prometheus.Desc
	acquireDuration         *prometheus.Desc
	newConnsCount           *prometheus.Desc
	maxLifetimeDestroyCount *prometheus.Desc
	maxIdleDestroyCount     *prometheus.Desc
}

func newPgxPoolCollector(pool *pgxpool.Pool, name string) *pgxPoolCollector {
	labels := prometheus.Labels{"app": "justrag", "pool": name}
	desc := func(metric, help string) *prometheus.Desc {
		return prometheus.NewDesc("pgxpool_"+metric, help, nil, labels)
	}
	return &pgxPoolCollector{
		pool:                    pool,
		name:                    name,
		totalConns:              desc("total_conns", "Current total connections in the pool (idle + in-use + constructing)."),
		idleConns:               desc("idle_conns", "Current idle connections in the pool."),
		acquiredConns:           desc("acquired_conns", "Current in-use connections."),
		constructingConns:       desc("constructing_conns", "Connections currently being constructed."),
		maxConns:                desc("max_conns", "Configured maximum number of connections."),
		acquireCount:            desc("acquire_count_total", "Cumulative successful pool acquisitions."),
		emptyAcquireCount:       desc("empty_acquire_count_total", "Cumulative acquisitions that had to wait for a connection. Canary for pool saturation."),
		canceledAcquires:        desc("canceled_acquire_count_total", "Cumulative acquisitions canceled by the caller's context."),
		acquireDuration:         desc("acquire_duration_seconds_total", "Cumulative time spent waiting on connection acquisitions, in seconds."),
		newConnsCount:           desc("new_conns_count_total", "Cumulative new connections opened to the database."),
		maxLifetimeDestroyCount: desc("max_lifetime_destroys_total", "Cumulative connections destroyed because they exceeded MaxConnLifetime. Combined with new_conns_count, indicates pool churn."),
		maxIdleDestroyCount:     desc("max_idle_destroys_total", "Cumulative connections destroyed because they exceeded MaxConnIdleTime."),
	}
}

// Describe implements prometheus.Collector.
func (c *pgxPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.totalConns
	ch <- c.idleConns
	ch <- c.acquiredConns
	ch <- c.constructingConns
	ch <- c.maxConns
	ch <- c.acquireCount
	ch <- c.emptyAcquireCount
	ch <- c.canceledAcquires
	ch <- c.acquireDuration
	ch <- c.newConnsCount
	ch <- c.maxLifetimeDestroyCount
	ch <- c.maxIdleDestroyCount
}

// Collect implements prometheus.Collector.
func (c *pgxPoolCollector) Collect(ch chan<- prometheus.Metric) {
	if c.pool == nil {
		return
	}
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.constructingConns, prometheus.GaugeValue, float64(s.ConstructingConns()))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireCount, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.canceledAcquires, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, s.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(c.newConnsCount, prometheus.CounterValue, float64(s.NewConnsCount()))
	ch <- prometheus.MustNewConstMetric(c.maxLifetimeDestroyCount, prometheus.CounterValue, float64(s.MaxLifetimeDestroyCount()))
	ch <- prometheus.MustNewConstMetric(c.maxIdleDestroyCount, prometheus.CounterValue, float64(s.MaxIdleDestroyCount()))
}

// RegisterPgxPoolStats registers a Prometheus collector that exports
// pgxpool.Stat() data on each scrape. name is a stable label distinguishing
// pools (e.g. "main", "vector"). Calling twice with the same name returns
// prometheus.AlreadyRegisteredError; callers (see infra.go) treat that as a
// soft warning rather than fatal. Invoke once per pool, at startup.
func RegisterPgxPoolStats(pool *pgxpool.Pool, name string) error {
	return prometheus.Register(newPgxPoolCollector(pool, name))
}
