-- Supports internal/api/metrics.go's SyncEventCounts GROUP BY (trigger,
-- result) — without this, that query is a full sequential scan of an
-- ever-growing, never-pruned table (§5.2) on every cache-miss scrape.
--
-- CONCURRENTLY so building it doesn't hold a lock that blocks sync_events
-- writes (the reconcile loop's own in-flight deploys) for however long the
-- build takes on an already-populated table — applied separately from the
-- rest of Schema (see store.Apply) since CONCURRENTLY can't run inside a
-- transaction block, which the other migrations' combined statement is.
CREATE INDEX CONCURRENTLY IF NOT EXISTS sync_events_trigger_result_idx ON sync_events (trigger, result);
