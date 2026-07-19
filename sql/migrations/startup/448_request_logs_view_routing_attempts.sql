-- Migration: 448_request_logs_view_routing_attempts
-- Purpose: 重建 request_logs_with_current_month，暴露 routing_attempts /
--          routing_summary，修复 GET /api/logs/:id 500。
-- Date: 2026-07-20
--
-- Root cause:
--   V350 已对 hot + parent ADD COLUMN，但 VIEW 的 SELECT * 在 CREATE 时
--   展开列清单，ADD COLUMN 后 VIEW 不会自动包含新列。
--   → ERROR: column rl.routing_attempts does not exist (SQLSTATE 42703)
--
-- Constraint:
--   hot / parent 存在多列类型漂移（customer_id text vs bigint 等），
--   不能用 SELECT * UNION ALL。策略：保留现有 VIEW 列集，仅追加
--   routing_attempts / routing_summary（两侧类型均为 jsonb / text）。
--
-- Idempotent: YES

BEGIN;

-- 1. 确保两侧有 routing 列（幂等）
ALTER TABLE request_logs
  ADD COLUMN IF NOT EXISTS routing_attempts jsonb,
  ADD COLUMN IF NOT EXISTS routing_summary text;

ALTER TABLE request_logs_hot
  ADD COLUMN IF NOT EXISTS routing_attempts jsonb,
  ADD COLUMN IF NOT EXISTS routing_summary text;

COMMENT ON COLUMN request_logs.routing_attempts IS
  '路由尝试序列（JSONB）。见 V350 / docs/design/routing-attempts-tracking/。';
COMMENT ON COLUMN request_logs.routing_summary IS
  '路由尝试人类可读摘要。见 V350。';
COMMENT ON COLUMN request_logs_hot.routing_attempts IS
  '路由尝试序列（JSONB）。见 V350 / docs/design/routing-attempts-tracking/。';
COMMENT ON COLUMN request_logs_hot.routing_summary IS
  '路由尝试人类可读摘要。见 V350。';

-- 2. 基于现有 VIEW 列清单追加 routing_*，避免牵动类型漂移列
DO $$
DECLARE
  base_cols text;
  final_cols text;
  view_exists boolean;
  already_has boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF NOT view_exists THEN
    -- 冷启动兜底：仅用两侧同名且同类型的列 + routing_*
    SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
      INTO base_cols
    FROM pg_attribute h
    JOIN pg_class ch ON h.attrelid = ch.oid
    JOIN pg_attribute p ON p.attname = h.attname
    JOIN pg_class cp ON p.attrelid = cp.oid
    WHERE ch.relname = 'request_logs_hot'
      AND cp.relname = 'request_logs'
      AND h.attnum > 0 AND NOT h.attisdropped
      AND p.attnum > 0 AND NOT p.attisdropped
      AND h.atttypid = p.atttypid
      AND h.attname NOT IN ('routing_attempts', 'routing_summary');

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '448: cannot build fallback column list';
    END IF;
    final_cols := base_cols || ', routing_attempts, routing_summary';
  ELSE
    SELECT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_name = 'request_logs_with_current_month'
        AND column_name = 'routing_attempts'
    ) INTO already_has;

    IF already_has THEN
      RAISE NOTICE '448: VIEW already exposes routing_attempts — skip recreate';
      RETURN;
    END IF;

    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
    FROM pg_attribute a
    JOIN pg_class c ON a.attrelid = c.oid
    WHERE c.relname = 'request_logs_with_current_month'
      AND a.attnum > 0 AND NOT a.attisdropped
      AND a.attname NOT IN ('routing_attempts', 'routing_summary');

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '448: existing VIEW has no columns';
    END IF;
    final_cols := base_cols || ', routing_attempts, routing_summary';
  END IF;

  DROP VIEW IF EXISTS request_logs_with_current_month;

  EXECUTE format($sql$
    CREATE VIEW request_logs_with_current_month AS
    SELECT %s FROM request_logs_hot
    UNION ALL
    SELECT %s FROM request_logs
  $sql$, final_cols, final_cols);

  COMMENT ON VIEW request_logs_with_current_month IS
    'Hot + monthly partitions UNION. Recreated by migration 448 (2026-07-20) '
    'to expose routing_attempts / routing_summary after V350 ADD COLUMN. '
    'Preserves prior VIEW column set to avoid hot/parent type drift.';
END $$;

-- 3. 验证
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'request_logs_with_current_month'
      AND column_name = 'routing_attempts'
  ) THEN
    RAISE EXCEPTION '448: VIEW missing routing_attempts after recreate';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'request_logs_with_current_month'
      AND column_name = 'routing_summary'
  ) THEN
    RAISE EXCEPTION '448: VIEW missing routing_summary after recreate';
  END IF;

  -- smoke: 能选出 routing 列
  PERFORM routing_attempts, routing_summary
  FROM request_logs_with_current_month
  LIMIT 1;

  RAISE NOTICE 'Migration 448 completed: VIEW exposes routing_attempts/summary';
END $$;

COMMIT;
