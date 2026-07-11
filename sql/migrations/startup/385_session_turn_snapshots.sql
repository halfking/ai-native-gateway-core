-- 385_session_turn_snapshots.sql
-- Per-turn audit snapshots for original, compressed and security-processed
-- conversation stages. One row always represents one request round so all
-- six directions remain aligned and can be evicted as a unit.

CREATE TABLE IF NOT EXISTS public.session_turn_snapshots (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    gw_session_id VARCHAR(128) NOT NULL,
    turn_no INT NOT NULL,
    request_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,

    original_send JSONB,
    original_receive JSONB,
    compressed_send JSONB,
    compressed_receive JSONB,
    secured_send JSONB,
    secured_receive JSONB,

    original_send_ref VARCHAR(64),
    original_receive_ref VARCHAR(64),
    compressed_send_ref VARCHAR(64),
    compressed_receive_ref VARCHAR(64),
    secured_send_ref VARCHAR(64),
    secured_receive_ref VARCHAR(64),

    compression_strategy TEXT,
    compression_meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    security_tags TEXT[] NOT NULL DEFAULT '{}',
    compressed_range_start INT,
    compressed_range_end INT,
    summary_marker TEXT,
    token_original INT NOT NULL DEFAULT 0,
    token_compressed INT NOT NULL DEFAULT 0,
    token_secured INT NOT NULL DEFAULT 0,
    stream_completed BOOLEAN NOT NULL DEFAULT TRUE,

    CONSTRAINT uq_session_turn_snapshot UNIQUE (tenant_id, gw_session_id, turn_no),
    CONSTRAINT uq_session_turn_snapshot_request UNIQUE (tenant_id, request_id)
);

CREATE INDEX IF NOT EXISTS idx_session_turn_snapshots_session
    ON public.session_turn_snapshots (tenant_id, gw_session_id, turn_no DESC);
CREATE INDEX IF NOT EXISTS idx_session_turn_snapshots_expiry
    ON public.session_turn_snapshots (expires_at);

ALTER TABLE public.session_turn_snapshots ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS session_turn_snapshots_tenant_isolation ON public.session_turn_snapshots;
CREATE POLICY session_turn_snapshots_tenant_isolation ON public.session_turn_snapshots
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS session_turn_snapshots_super_admin_bypass ON public.session_turn_snapshots;
CREATE POLICY session_turn_snapshots_super_admin_bypass ON public.session_turn_snapshots
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE public.session_turn_snapshots IS
    'TTL-bound, turn-aligned original/compressed/secured conversation snapshots for admin audit.';
