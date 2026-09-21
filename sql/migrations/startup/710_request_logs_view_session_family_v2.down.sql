-- Rollback Migration 710: request_logs_with_current_month session-family v2 body.
--
-- Restores the v1 canonical view body (700 shape): hot ∪ parent base wrapper
-- chain + request_class/due_at/system_fingerprint/raw_model_name laterals —
-- exactly the pre-710 production shape. The dual-shape guard mirrors 700: on
-- a dynamically rebuilt chain (680/self-heal on a post-603 database) the base
-- wrapper already carries system_fingerprint/raw_model_name and an
-- unconditional lateral re-add would fail with "column already exists", so
-- the select list is probed first.
--
-- The schema_migrations row is kept (append-only ledger convention).

BEGIN;

DO $$
DECLARE
  base_has_fp  boolean;
  base_has_raw boolean;
BEGIN
  SELECT
    EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public'
        AND table_name = 'request_logs_with_current_month_without_customer_id'
        AND column_name = 'system_fingerprint'
    ),
    EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public'
        AND table_name = 'request_logs_with_current_month_without_customer_id'
        AND column_name = 'raw_model_name'
    )
  INTO base_has_fp, base_has_raw;

  -- 42P16 守卫（2026-09-21 契约测试 down 链实测）：CREATE OR REPLACE VIEW
  -- 不能变更既有列类型。canonical 处于会话 v2 体（710/732）时 agent_name、
  -- origin_actor、client_protocol 等列自 session_turns 直出为 text，v1 体按
  -- request_logs 形态收窄为 varchar——OR REPLACE 直接 42P16 炸停回滚链。
  -- 先 DROP 再 CREATE（同事务原子替换；v_* 分析视图读 request_logs 表而非
  -- 本视图，无目录级依赖方）。
  DROP VIEW IF EXISTS public.request_logs_with_current_month;

  IF base_has_fp AND base_has_raw THEN
    -- 动态重建链（680 引导/自愈，基础交集自带 fp/raw）：canonical 仅追加
    -- customer_id + request_class/due_at。
    CREATE VIEW public.request_logs_with_current_month AS
    SELECT v.*, source.request_class, source.due_at
    FROM public.request_logs_with_current_month_without_request_class_due_at v
    LEFT JOIN LATERAL (
        SELECT h.request_class, h.due_at
        FROM public.request_logs_hot h
        WHERE h.request_id = v.request_id AND h.ts = v.ts
        UNION ALL
        SELECT p.request_class, p.due_at
        FROM public.request_logs p
        WHERE p.request_id = v.request_id AND p.ts = v.ts
        LIMIT 1
    ) source ON true;
  ELSE
    -- 冻结链（577/610 时代基础交集，本机与 252 生产现网形态）：按 700 体
    -- 追加 request_class/due_at/system_fingerprint/raw_model_name。
    CREATE VIEW public.request_logs_with_current_month AS
    SELECT v.*, source.request_class, source.due_at, source.system_fingerprint, source.raw_model_name
    FROM public.request_logs_with_current_month_without_request_class_due_at v
    LEFT JOIN LATERAL (
        SELECT h.request_class, h.due_at, h.system_fingerprint, h.raw_model_name
        FROM public.request_logs_hot h
        WHERE h.request_id = v.request_id AND h.ts = v.ts
        UNION ALL
        SELECT p.request_class, p.due_at, p.system_fingerprint, p.raw_model_name
        FROM public.request_logs p
        WHERE p.request_id = v.request_id AND p.ts = v.ts
        LIMIT 1
    ) source ON true;
  END IF;

  COMMENT ON VIEW public.request_logs_with_current_month IS
    'Hot + monthly partitions UNION with customer_id (577), request_class/due_at (610), '
    'system_fingerprint (696) and raw_model_name (700) appended. Bootstrap-recreated by '
    '680 / db.ensureRequestLogsCurrentMonthView when dropped out-of-band.';
END $$;

COMMIT;
