package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

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

	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		rows, err := status.ListApplications(ctx)
		if err != nil {
			return fmt.Errorf("metrics: list applications: %w", err)
		}
		statusCounts := map[string]int64{}
		healthCounts := map[string]int64{}
		for _, row := range rows {
			statusCounts[row.Status]++
			healthCounts[row.Health]++
		}
		for s, c := range statusCounts {
			o.ObserveInt64(syncStatus, c, metric.WithAttributes(attribute.String("status", s)))
		}
		for h, c := range healthCounts {
			o.ObserveInt64(healthStatus, c, metric.WithAttributes(attribute.String("health", h)))
		}

		eventCounts, err := status.SyncEventCounts(ctx)
		if err != nil {
			return fmt.Errorf("metrics: sync event counts: %w", err)
		}
		for _, ec := range eventCounts {
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
