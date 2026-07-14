CREATE TABLE IF NOT EXISTS runtime_metrics (
    id                  BIGSERIAL PRIMARY KEY,
    instance_id         TEXT NOT NULL,
    license_id          BIGINT REFERENCES licenses(id) ON DELETE SET NULL,
    timestamp           TIMESTAMPTZ NOT NULL DEFAULT now(),
    cpu_usage_pct       REAL,
    mem_used_mb         BIGINT,
    mem_total_mb        BIGINT,
    disk_used_gb        BIGINT,
    disk_total_gb       BIGINT,
    db_size_mb          BIGINT,
    uptime_secs         BIGINT,
    current_concurrency INT,
    last_5min_tps       REAL,
    last_5min_p50_ms    REAL,
    last_5min_p99_ms    REAL,
    last_5min_success_pct REAL,
    model_usage         JSONB,
    tenant_count        INT
);

CREATE INDEX IF NOT EXISTS idx_rt_instance_time
    ON runtime_metrics (instance_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_rt_time
    ON runtime_metrics (timestamp DESC);
