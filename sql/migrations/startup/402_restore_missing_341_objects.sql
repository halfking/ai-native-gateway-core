-- Migration 402: 恢复 migration 341 中意外丢失的数据库对象
--
-- 背景:
--   虽然 schema_migrations 中 341 标记为已应用（2026-07-05），
--   但以下对象在生产环境中丢失，导致运行时错误：
--     1. system_health_status(integer) 函数 → bg/system_health.go WARN
--     2. node_probe_state 表 → bg/node_probe.go WARN
--     3. model_offers 视图的 3 个 INSTEAD OF 触发器 → health_auto_recover ERROR
--
-- 根因分析:
--   可能是之前某次 DDL 操作（DROP SCHEMA/TABLE/FUNCTION）意外删除了这些对象。
--   migration 341 中定义了这些对象，但未建立依赖保护机制。
--
-- 修复策略:
--   使用 CREATE OR REPLACE / CREATE IF NOT EXISTS 幂等重建所有对象。
--   确保在 154 生产环境和后续新环境中这些对象始终存在。
--
-- 2026-07-15: 在 252 PG (172.16.2.210:5432) 手动应用后，创建此 migration
-- 以便后续环境自动应用。

-- ============================================================
-- 1) system_health_status(integer) — 30s 窗口健康状态
-- ============================================================
-- 来自 migration 341，用于 bg/system_health.go 和 /api/health/system

CREATE OR REPLACE FUNCTION system_health_status(
    p_window_seconds integer DEFAULT 30
) RETURNS TABLE (
    status          text,
    success_rate    numeric,
    sample_count    bigint,
    failure_count   bigint,
    last_check_at   timestamptz
)
LANGUAGE SQL
STABLE
AS $$
    WITH win AS (
        SELECT
            COUNT(*)::bigint                AS n,
            COUNT(*) FILTER (WHERE success)::bigint AS ok,
            COUNT(*) FILTER (WHERE NOT success)::bigint AS fail
        FROM request_logs_hot
        WHERE ts >= now() - make_interval(secs => p_window_seconds)
    )
    SELECT
        CASE
            WHEN n = 0                                  THEN 'suspect'
            WHEN (ok::numeric / NULLIF(n,0)) >= 0.80    THEN 'ok'
            ELSE 'degraded'
        END                                            AS status,
        ROUND( (ok::numeric / NULLIF(n,0))::numeric, 4) AS success_rate,
        n                                              AS sample_count,
        fail                                           AS failure_count,
        now()                                          AS last_check_at
    FROM win;
$$;

COMMENT ON FUNCTION system_health_status(integer) IS
'341: returns ok (>=80% success), degraded (<80%), or suspect (no traffic) over a sliding window. Consumed by bg/system_health.go and /api/health/system.';

-- ============================================================
-- 2) node_probe_state — per (credential, model) 探测状态机
-- ============================================================
-- 来自 migration 341，用于 bg/node_probe.go

CREATE TABLE IF NOT EXISTS node_probe_state (
    credential_id            bigint NOT NULL,
    raw_model_name           text   NOT NULL,
    consecutive_failures     integer NOT NULL DEFAULT 0,
    consecutive_successes    integer NOT NULL DEFAULT 0,
    last_attempt_at          timestamptz,
    next_retry_at            timestamptz NOT NULL DEFAULT now(),
    next_retry_seconds       integer NOT NULL DEFAULT 5,
    paused                   boolean NOT NULL DEFAULT false,    -- true after attempt 7
    last_run_id              bigint,
    last_direct_ok           boolean,
    last_gateway_ok          boolean,
    last_err_code            text,
    last_err_detail          text,
    in_flight_until          timestamptz,                       -- prevents same-key re-entry
    updated_at               timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, raw_model_name)
);

CREATE INDEX IF NOT EXISTS idx_node_probe_state_due
    ON node_probe_state (next_retry_at)
    WHERE paused = FALSE;

COMMENT ON TABLE node_probe_state IS
'341: per (credential, model) node-probe state machine. 7-step backoff ladder, paused after attempt=7 (24h cap).';

-- ============================================================
-- 3) model_offers 视图的 3 个 INSTEAD OF 触发器
-- ============================================================
-- 来自 baseline schema (sql/objects/triggers/model_offers_*.sql)
-- 这些触发器将对 model_offers 视图的 INSERT/UPDATE/DELETE 转换为对
-- credential_model_bindings + provider_models 的实际操作。
--
-- 注意: 触发器函数 (model_offers_insert_trigger, model_offers_update_trigger,
-- model_offers_delete_trigger) 已在 baseline schema 中定义，这里仅重建触发器。

DO $$
BEGIN
    -- 先删除可能存在的旧触发器（避免重复）
    DROP TRIGGER IF EXISTS model_offers_insert ON model_offers;
    DROP TRIGGER IF EXISTS model_offers_update ON model_offers;
    DROP TRIGGER IF EXISTS model_offers_delete ON model_offers;
END $$;

CREATE TRIGGER model_offers_insert
    INSTEAD OF INSERT ON model_offers
    FOR EACH ROW
    EXECUTE FUNCTION model_offers_insert_trigger();

CREATE TRIGGER model_offers_update
    INSTEAD OF UPDATE ON model_offers
    FOR EACH ROW
    EXECUTE FUNCTION model_offers_update_trigger();

CREATE TRIGGER model_offers_delete
    INSTEAD OF DELETE ON model_offers
    FOR EACH ROW
    EXECUTE FUNCTION model_offers_delete_trigger();

-- ============================================================
-- 4) 验证
-- ============================================================

DO $$
DECLARE
    fn_count int := 0;
    tbl_count int := 0;
    trg_count int := 0;
BEGIN
    -- 验证函数
    SELECT COUNT(*) INTO fn_count FROM pg_proc WHERE proname = 'system_health_status';
    IF fn_count = 0 THEN
        RAISE EXCEPTION 'Migration 402: system_health_status function not found';
    END IF;

    -- 验证表
    SELECT COUNT(*) INTO tbl_count FROM information_schema.tables WHERE table_name = 'node_probe_state';
    IF tbl_count = 0 THEN
        RAISE EXCEPTION 'Migration 402: node_probe_state table not found';
    END IF;

    -- 验证触发器
    SELECT COUNT(*) INTO trg_count FROM pg_trigger 
    WHERE tgrelid = 'model_offers'::regclass 
      AND tgname IN ('model_offers_insert', 'model_offers_update', 'model_offers_delete');
    IF trg_count < 3 THEN
        RAISE EXCEPTION 'Migration 402: model_offers triggers incomplete (found %, expected 3)', trg_count;
    END IF;

    RAISE NOTICE 'Migration 402 completed: restored system_health_status(), node_probe_state, and 3 model_offers triggers';
END $$;
