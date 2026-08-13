-- Backfill V2 sessions from request_logs (V1) — v2 (per-session, batched)
-- Idempotent: re-runs produce no duplicate turns (NOT EXISTS + ON CONFLICT).
-- Marks source_kind='backfill', quality='inferred'.
--
-- Usage:
--   psql "$DB" -f sql/scripts/backfill_sessions_v2_v2.sql
--     -- installs the function below; no DDL is destructive.
--
--   -- then invoke per session:
--   SELECT backfill_session_v2_turns('tenant_xxx', 'gw_abc...', 100);
--
-- Notes (column reality, verified against deploy/sql/objects/tables/request_logs.sql):
--   - request_logs has no `meta` jsonb column; submit_mode is not stored on V1.
--     We default to 'full' as the safe assumption (COALESCE).
--   - request_logs has `success` (bool) and `request_status` (text), NOT `status_code`.
--     We map success → status_code (200/0) for backwards compat with public.session_turns.
--   - request_logs is partitioned by ts (TIMESTAMPTZ); partition_date derived from ts.
--   - public.session_turns has UNIQUE (request_id, partition_date) so ON CONFLICT is required.
--
-- Author: llm-gateway-ops
-- Date: 2026-07-24

CREATE OR REPLACE FUNCTION backfill_session_v2_turns(
    p_tenant_id TEXT,
    p_session_id TEXT,
    p_batch_size INT DEFAULT 100
) RETURNS INT LANGUAGE plpgsql AS $$
DECLARE
    v_count INT;
BEGIN
    INSERT INTO public.session_turns (
        session_id,
        turn_no,
        tenant_id,
        request_id,
        ts,
        submit_mode,
        source_kind,
        quality,
        model,
        provider,
        prompt_tokens,
        completion_tokens,
        cost_usd,
        latency_ms,
        status_code,
        success,
        partition_date
    )
    SELECT
        rl.gw_session_id                                                AS session_id,
        ROW_NUMBER() OVER (PARTITION BY rl.gw_session_id ORDER BY rl.ts ASC) AS turn_no,
        rl.tenant_id::TEXT                                              AS tenant_id,
        rl.request_id                                                   AS request_id,
        rl.ts                                                           AS ts,
        'full'                                                          AS submit_mode,
        'backfill'                                                      AS source_kind,
        'inferred'                                                      AS quality,
        rl.client_model                                                 AS model,
        rl.provider_id::TEXT                                            AS provider,
        rl.prompt_tokens,
        rl.completion_tokens,
        rl.cost_usd,
        rl.latency_ms,
        CASE WHEN rl.success THEN 200 ELSE 0 END                       AS status_code,
        rl.success,
        (rl.ts AT TIME ZONE 'UTC')::DATE                                AS partition_date
    FROM public.request_logs rl
    WHERE rl.gw_session_id = p_session_id
      AND rl.tenant_id::TEXT = p_tenant_id
      AND rl.gw_session_id IS NOT NULL
      AND NOT EXISTS (
          SELECT 1
          FROM public.session_turns st
          WHERE st.session_id   = rl.gw_session_id
            AND st.request_id   = rl.request_id
      )
    ORDER BY rl.ts ASC
    LIMIT GREATEST(p_batch_size, 1)
    ON CONFLICT (request_id, partition_date) DO NOTHING;

    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
$$;

COMMENT ON FUNCTION backfill_session_v2_turns(TEXT, TEXT, INT) IS
    'Backfill a single session''s turns from public.request_logs into public.session_turns.
     Idempotent via NOT EXISTS + ON CONFLICT DO NOTHING.
     Marks source_kind=backfill, quality=inferred.';