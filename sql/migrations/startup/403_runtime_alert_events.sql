CREATE TABLE IF NOT EXISTS runtime_alert_events (
    id                BIGSERIAL PRIMARY KEY,
    rule_key          TEXT NOT NULL,
    instance_id       TEXT NOT NULL,
    severity          TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    title             TEXT NOT NULL,
    message           TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'triggered'
        CHECK (status IN ('triggered', 'acknowledged', 'resolved', 'suppressed')),
    metric_value      DOUBLE PRECISION,
    detected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    acked_at          TIMESTAMPTZ,
    acked_by          TEXT,
    resolved_at       TIMESTAMPTZ,
    resolved_by       TEXT,
    suppressed_until  TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_rae_instance_status
    ON runtime_alert_events (instance_id, status, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_rae_rule_open
    ON runtime_alert_events (rule_key, instance_id)
    WHERE status IN ('triggered', 'acknowledged', 'suppressed');
