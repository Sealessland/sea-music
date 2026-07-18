package appapi

import (
	"context"
	"database/sql"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/social"
)

type OperationalMetrics struct {
	eventBacklog      *events.PostgresRepository
	counterReconciler *social.CounterReconciler
	database          *sql.DB
	redis             *redis.Client
	outboxEvents      *prometheus.Desc
	outboxOldest      *prometheus.Desc
	reconciliations   *prometheus.Desc
	counterDrift      *prometheus.Desc
	sqlConnections    *prometheus.Desc
	processingJobs    *prometheus.Desc
	redisConnections  *prometheus.Desc
	redisHits         *prometheus.Desc
	redisMisses       *prometheus.Desc
	redisTimeouts     *prometheus.Desc
}

func NewOperationalMetrics(eventBacklog *events.PostgresRepository, counterReconciler *social.CounterReconciler, database *sql.DB, redisClient *redis.Client) *OperationalMetrics {
	return &OperationalMetrics{
		eventBacklog: eventBacklog, counterReconciler: counterReconciler, database: database, redis: redisClient,
		outboxEvents:     prometheus.NewDesc("sea_music_outbox_events", "Current number of outbox events by state.", []string{"state"}, nil),
		outboxOldest:     prometheus.NewDesc("sea_music_outbox_oldest_seconds", "Age of the oldest pending outbox event.", nil, nil),
		reconciliations:  prometheus.NewDesc("sea_music_counter_reconciliations_total", "Total counter reconciliation repairs.", nil, nil),
		counterDrift:     prometheus.NewDesc("sea_music_counter_drift_total", "Total counter drift repaired.", nil, nil),
		sqlConnections:   prometheus.NewDesc("sea_music_sql_connections", "SQL connections by state.", []string{"state"}, nil),
		processingJobs:   prometheus.NewDesc("sea_music_processing_jobs", "Current media processing jobs by state.", []string{"state"}, nil),
		redisConnections: prometheus.NewDesc("sea_music_redis_connections", "Redis connections by state.", []string{"state"}, nil),
		redisHits:        prometheus.NewDesc("sea_music_redis_pool_hits_total", "Total Redis pool hits.", nil, nil),
		redisMisses:      prometheus.NewDesc("sea_music_redis_pool_misses_total", "Total Redis pool misses.", nil, nil),
		redisTimeouts:    prometheus.NewDesc("sea_music_redis_pool_timeouts_total", "Total Redis pool timeouts.", nil, nil),
	}
}

func (metrics *OperationalMetrics) Describe(descriptions chan<- *prometheus.Desc) {
	for _, description := range []*prometheus.Desc{metrics.outboxEvents, metrics.outboxOldest, metrics.reconciliations, metrics.counterDrift, metrics.sqlConnections, metrics.processingJobs, metrics.redisConnections, metrics.redisHits, metrics.redisMisses, metrics.redisTimeouts} {
		descriptions <- description
	}
}

func (metrics *OperationalMetrics) Collect(output chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if stats, err := metrics.eventBacklog.Backlog(ctx); err == nil {
		output <- prometheus.MustNewConstMetric(metrics.outboxEvents, prometheus.GaugeValue, float64(stats.Pending), "pending")
		output <- prometheus.MustNewConstMetric(metrics.outboxEvents, prometheus.GaugeValue, float64(stats.Publishing), "publishing")
		output <- prometheus.MustNewConstMetric(metrics.outboxEvents, prometheus.GaugeValue, float64(stats.Failed), "failed")
		output <- prometheus.MustNewConstMetric(metrics.outboxOldest, prometheus.GaugeValue, stats.OldestSeconds)
	}
	if stats, err := metrics.counterReconciler.Stats(ctx); err == nil {
		output <- prometheus.MustNewConstMetric(metrics.reconciliations, prometheus.CounterValue, float64(stats.Repairs))
		output <- prometheus.MustNewConstMetric(metrics.counterDrift, prometheus.CounterValue, float64(stats.DriftTotal))
	}
	databaseStats := metrics.database.Stats()
	output <- prometheus.MustNewConstMetric(metrics.sqlConnections, prometheus.GaugeValue, float64(databaseStats.OpenConnections), "open")
	output <- prometheus.MustNewConstMetric(metrics.sqlConnections, prometheus.GaugeValue, float64(databaseStats.InUse), "in_use")
	output <- prometheus.MustNewConstMetric(metrics.sqlConnections, prometheus.GaugeValue, float64(databaseStats.Idle), "idle")
	if rows, err := metrics.database.QueryContext(ctx, `SELECT state, count(*) FROM video.processing_jobs GROUP BY state`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var state string
			var count float64
			if rows.Scan(&state, &count) == nil {
				output <- prometheus.MustNewConstMetric(metrics.processingJobs, prometheus.GaugeValue, count, state)
			}
		}
	}
	redisStats := metrics.redis.PoolStats()
	output <- prometheus.MustNewConstMetric(metrics.redisConnections, prometheus.GaugeValue, float64(redisStats.TotalConns), "total")
	output <- prometheus.MustNewConstMetric(metrics.redisConnections, prometheus.GaugeValue, float64(redisStats.IdleConns), "idle")
	output <- prometheus.MustNewConstMetric(metrics.redisHits, prometheus.CounterValue, float64(redisStats.Hits))
	output <- prometheus.MustNewConstMetric(metrics.redisMisses, prometheus.CounterValue, float64(redisStats.Misses))
	output <- prometheus.MustNewConstMetric(metrics.redisTimeouts, prometheus.CounterValue, float64(redisStats.Timeouts))
}
