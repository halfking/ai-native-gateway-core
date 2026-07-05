-- 004_tuning_signals.sql — Implicit feedback signals for auto-route tuning.
--
-- tuning_signals captures a per-request quality score derived from
-- implicit signals (success, latency, cost, session drift). The feedback
-- analyzer (Phase 4) aggregates these to generate keyword and weight
-- tuning proposals.
--
-- Write path: relay/request_log_pipeline.go asynchronously inserts a row
-- after each auto-route request completes. Uses the telemetry batching
-- channel (zero hot-path blocking).
--
-- Idempotent: safe to run multiple times.

BEGIN;

CREATE TABLE IF NOT EXISTS tuning_signals (
    id              BIGSERIAL PRIMARY KEY,
    request_id      TEXT NOT NULL,
    session_id      TEXT,
    ts              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    task_type       TEXT NOT NULL,
    classifier      TEXT NOT NULL,
        -- 'heuristic' | 'llm' | 'session_cache' | 'default'
    confidence      NUMERIC(4,3),
    chosen_model    TEXT,
    canonical_id    INT,

    -- Implicit feedback signals (each normalised to 0-1, higher = better)
    success_score   NUMERIC(3,2) NOT NULL DEFAULT 0.5,
        -- 1.0 = success, 0.0 = failure, 0.5 = partial (stream interrupted)
    latency_score   NUMERIC(3,2) NOT NULL DEFAULT 0.5,
        -- 1 - (latency_ms / task_type_p95_baseline), clamped [0,1]
    cost_score      NUMERIC(3,2) NOT NULL DEFAULT 0.5,
        -- 1 - (cost / task_type_p75_baseline), clamped [0,1]
    drift_flag      BOOLEAN NOT NULL DEFAULT FALSE,
        -- TRUE when session's previous request had a different task_type
        -- AND the change wasn't from a hard override (vision/long_context/agent)

    -- Composite quality score: weighted blend of the four signals
    -- Formula: 0.4*success + 0.3*latency + 0.2*cost + 0.1*(1-drift)
    quality_score   NUMERIC(3,2) NOT NULL DEFAULT 0.5,

    -- Raw metrics for audit (denormalised from request_logs for fast queries)
    latency_ms      INT,
    cost_usd        NUMERIC(10,6),
    prompt_tokens   INT,
    completion_tokens INT,

    -- Full auto_decision snapshot for deep analysis
    signal_payload  JSONB,
        -- Contains: {candidates_top3, last_user_prompt_preview, reason, ...}

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Primary query index: feedback analyzer groups by (task_type, ts)
CREATE INDEX IF NOT EXISTS idx_tuning_signals_task_ts
    ON tuning_signals (task_type, ts DESC);

-- Secondary index: session-drift detection
CREATE INDEX IF NOT EXISTS idx_tuning_signals_session
    ON tuning_signals (session_id, ts DESC)
    WHERE session_id IS NOT NULL;

-- Low-quality filter for keyword discovery (Phase 4)
CREATE INDEX IF NOT EXISTS idx_tuning_signals_lowq
    ON tuning_signals (task_type, ts DESC)
    WHERE quality_score < 0.5 AND classifier = 'heuristic';

COMMENT ON TABLE tuning_signals IS
    'Implicit feedback signals for auto-route tuning. Written async per-request, analyzed daily by feedback_analyzer.';

COMMIT;
