package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// metricsCacheTTL bounds how often a scrape actually re-queries Postgres.
// sync_events is append-only and never pruned (§5.2) — SyncEventCounts'
// GROUP BY is a full scan of an ever-growing table, so a scraper hitting
// /metrics faster than this (Prometheus defaults to a 15s-60s
// scrape_interval, but nothing stops a misconfigured one from polling much
// faster) would otherwise re-scan the whole table on every single request.
const metricsCacheTTL = 15 * time.Second

// metricsSnapshot is what one (possibly cached) collection observes.
type metricsSnapshot struct {
	statusCounts map[string]int64
	healthCounts map[string]int64
	eventCounts  []SyncEventCount
}

type metricsCache struct {
	mu        sync.Mutex
	expiresAt time.Time
	snapshot  metricsSnapshot
}

// metricsQueryTimeout bounds the underlying Postgres queries — without
// this, a single wedged connection would hold metricsCache's mutex
// indefinitely, blocking every future scrape (even after the cache TTL
// expires), not just the one that hit the hang.
const metricsQueryTimeout = 10 * time.Second

func (c *metricsCache) get(ctx context.Context, status StatusStore) (metricsSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expiresAt) {
		return c.snapshot, nil
	}

	ctx, cancel := context.WithTimeout(ctx, metricsQueryTimeout)
	defer cancel()

	rows, err := status.ListApplications(ctx)
	if err != nil {
		return metricsSnapshot{}, fmt.Errorf("metrics: list applications: %w", err)
	}
	statusCounts := map[string]int64{}
	healthCounts := map[string]int64{}
	for _, row := range rows {
		statusCounts[row.Status]++
		healthCounts[row.Health]++
	}

	eventCounts, err := status.SyncEventCounts(ctx)
	if err != nil {
		return metricsSnapshot{}, fmt.Errorf("metrics: sync event counts: %w", err)
	}

	c.snapshot = metricsSnapshot{statusCounts: statusCounts, healthCounts: healthCounts, eventCounts: eventCounts}
	c.expiresAt = time.Now().Add(metricsCacheTTL)
	return c.snapshot, nil
}

// NewMetricsHandler wires OTel metric instruments — status/health snapshot
// gauges and a cumulative sync-events counter — to callbacks that read the
// same applications/sync_events data the dashboard already reads, and
// returns the Prometheus-exposition-format HTTP handler that serves them
// (§ metrics endpoint).
//
// These are all observable (callback-driven) instruments, not incremented
// in-process: sync_events/applications live in Postgres shared across every
// controller replica, so the correct value at scrape time is "read the
// shared source of truth right now," not a per-replica in-memory tally that
// would reset on restart and disagree between replicas.
//
// A dedicated prometheus.Registry, not the global default one: each
// Handler (e.g. each test's own fixture) needs its own instrument
// registration rather than panicking on a duplicate collector against a
// shared global registry.
func NewMetricsHandler(status StatusStore) (http.Handler, error) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprometheus.New(otelprometheus.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("create prometheus exporter: %w", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	meter := provider.Meter("runcd")

	syncStatus, err := meter.Int64ObservableGauge("runcd_sync_status_total",
		metric.WithDescription("Sync units currently in each status."))
	if err != nil {
		return nil, fmt.Errorf("create runcd_sync_status_total: %w", err)
	}
	healthStatus, err := meter.Int64ObservableGauge("runcd_health_status_total",
		metric.WithDescription("Sync units currently in each health state."))
	if err != nil {
		return nil, fmt.Errorf("create runcd_health_status_total: %w", err)
	}
	syncEvents, err := meter.Int64ObservableCounter("runcd_sync_events_total",
		metric.WithDescription("Total sync attempts recorded, by trigger and result."))
	if err != nil {
		return nil, fmt.Errorf("create runcd_sync_events_total: %w", err)
	}

	cache := &metricsCache{}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		snap, err := cache.get(ctx, status)
		if err != nil {
			return err
		}
		for s, c := range snap.statusCounts {
			o.ObserveInt64(syncStatus, c, metric.WithAttributes(attribute.String("status", s)))
		}
		for h, c := range snap.healthCounts {
			o.ObserveInt64(healthStatus, c, metric.WithAttributes(attribute.String("health", h)))
		}
		for _, ec := range snap.eventCounts {
			o.ObserveInt64(syncEvents, ec.Count,
				metric.WithAttributes(attribute.String("trigger", ec.Trigger), attribute.String("result", ec.Result)))
		}
		return nil
	}, syncStatus, healthStatus, syncEvents)
	if err != nil {
		return nil, fmt.Errorf("register metrics callback: %w", err)
	}

	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), nil
}
