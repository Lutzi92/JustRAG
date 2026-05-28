package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// redisPoolStatsSource is the narrow interface a redis client exposes to this
// collector: just the per-scrape PoolStats() snapshot. Decouples the collector
// from the *redis.Client / asynq.Client concrete types so both pools can be
// registered the same way.
type redisPoolStatsSource interface {
	PoolStats() *redis.PoolStats
}

// redisPoolCollector exports go-redis PoolStats() to Prometheus on each scrape.
// Hits/Misses/Timeouts/StaleConns are the canaries for pool saturation and
// connection churn — symmetric with the pgxpool collector.
type redisPoolCollector struct {
	src  redisPoolStatsSource
	name string

	totalConns *prometheus.Desc
	idleConns  *prometheus.Desc
	staleConns *prometheus.Desc
	hits       *prometheus.Desc
	misses     *prometheus.Desc
	timeouts   *prometheus.Desc
}

func newRedisPoolCollector(src redisPoolStatsSource, name string) *redisPoolCollector {
	labels := prometheus.Labels{"app": "justrag", "pool": name}
	desc := func(metric, help string) *prometheus.Desc {
		return prometheus.NewDesc("redis_pool_"+metric, help, nil, labels)
	}
	return &redisPoolCollector{
		src:        src,
		name:       name,
		totalConns: desc("total_conns", "Current total connections in the pool (idle + in-use)."),
		idleConns:  desc("idle_conns", "Current idle connections in the pool."),
		staleConns: desc("stale_conns_total", "Cumulative connections removed from the pool because they were stale."),
		hits:       desc("hits_total", "Cumulative acquisitions that found a free connection in the pool."),
		misses:     desc("misses_total", "Cumulative acquisitions that did NOT find a free connection. Canary for pool saturation."),
		timeouts:   desc("timeouts_total", "Cumulative pool wait timeouts."),
	}
}

func (c *redisPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.totalConns
	ch <- c.idleConns
	ch <- c.staleConns
	ch <- c.hits
	ch <- c.misses
	ch <- c.timeouts
}

func (c *redisPoolCollector) Collect(ch chan<- prometheus.Metric) {
	if c.src == nil {
		return
	}
	s := c.src.PoolStats()
	if s == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(s.TotalConns))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(s.IdleConns))
	ch <- prometheus.MustNewConstMetric(c.staleConns, prometheus.CounterValue, float64(s.StaleConns))
	ch <- prometheus.MustNewConstMetric(c.hits, prometheus.CounterValue, float64(s.Hits))
	ch <- prometheus.MustNewConstMetric(c.misses, prometheus.CounterValue, float64(s.Misses))
	ch <- prometheus.MustNewConstMetric(c.timeouts, prometheus.CounterValue, float64(s.Timeouts))
}

// RegisterRedisPoolStats registers a Prometheus collector that exports go-redis
// pool statistics on each scrape. name is a stable label distinguishing pools
// (e.g. "main", "asynq"). Calling twice with the same name returns
// prometheus.AlreadyRegisteredError; callers treat that as a soft warning.
// Invoke once per pool, at startup.
func RegisterRedisPoolStats(src redisPoolStatsSource, name string) error {
	return prometheus.Register(newRedisPoolCollector(src, name))
}
