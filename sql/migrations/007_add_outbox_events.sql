CREATE TABLE IF NOT EXISTS outbox_events (
    id BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL,
    job_id TEXT NOT NULL UNIQUE,
    stream_key TEXT NOT NULL DEFAULT 'deepoj:jobs',
    enqueue_ts BIGINT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT,
    dispatched_at TIMESTAMPTZ,
    stream_entry_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_outbox_pending_retry
    ON outbox_events (next_attempt_at, id)
    WHERE dispatched_at IS NULL AND status = 'pending';

CREATE INDEX IF NOT EXISTS idx_outbox_job_id
    ON outbox_events (job_id);

-- 回滚步骤（手动）:
-- DROP INDEX IF EXISTS idx_outbox_pending_retry;
-- DROP INDEX IF EXISTS idx_outbox_job_id;
-- DROP TABLE IF EXISTS outbox_events;
