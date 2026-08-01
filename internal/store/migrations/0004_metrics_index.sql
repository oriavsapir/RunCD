-- Supports internal/api/metrics.go's SyncEventCounts GROUP BY (trigger,
-- result) — without this, that query is a full sequential scan of an
-- ever-growing, never-pruned table (§5.2) on every cache-miss scrape.
CREATE INDEX IF NOT EXISTS sync_events_trigger_result_idx ON sync_events (trigger, result);
