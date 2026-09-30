-- Migration 510: Add request_type to request_logs* (V3.2 主从请求分类)
--
-- 日期: 2026-08-13
--
-- Purpose
-- ───────
-- 会话优化 V3.2 需要在首页"实时请求流"区分主请求与扩展请求
-- （标题生成 / 总结 / 敏感词检查 / 压缩等），并把扩展请求挂到父请求下展示。
--
-- 现状（rule 42 代码先行）:
--   parent_request_id 与 origin_actor 已存在（见 013/341/466），
--   本迁移**不重复添加**，只新增 request_type 分类列。
--
-- Column:
--   request_type TEXT NOT NULL DEFAULT 'main'
--   CHECK 枚举: main / title_gen / summary / sensitive_check / compression / other
--
-- Tables:
--   request_logs_hot  — write path (telemetry INSERT)
--   request_logs      — parent partitioned table (promote SELECT * requires parity)
--
-- View freeze (rule 49 §9.2 / rule 38 §5.1 2b):
--   Rebuild request_logs_with_current_month using the 448/459/491 append pattern
--   so admin SELECTs that project request_type do not 42703.
--
-- Idempotent: YES (IF NOT EXISTS + view early-return)
-- Down: 510_request_type.down.sql
-- Breaking: NO (有默认值，存量行自动为 'main')

BEGIN;

-- 1. Column on hot + parent (same order → promote SELECT * stays aligned)
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS request_type TEXT NOT NULL DEFAULT 'main';

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS request_type TEXT NOT NULL DEFAULT 'main';

-- CHECK 约束（幂等：先删再加）
ALTER TABLE request_logs_hot
    DROP CONSTRAINT IF EXISTS chk_request_logs_hot_request_type;
ALTER TABLE request_logs_hot
    ADD CONSTRAINT chk_request_logs_hot_request_type
    CHECK (request_type IN ('main','title_gen','summary','sensitive_check','compression','other'));

ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS chk_request_logs_request_type;
ALTER TABLE request_logs
    ADD CONSTRAINT chk_request_logs_request_type
    CHECK (request_type IN ('main','title_gen','summary','sensitive_check','compression','other'));

COMMENT ON COLUMN request_logs_hot.request_type IS
  'V3.2 (2026-08-13): 请求类型。main=主请求；title_gen/summary/sensitive_check/compression=扩展请求，'
  '通过 parent_request_id 挂到主请求下展示。';

-- 非主请求查询索引（hot only，低写影响）
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_request_type
    ON request_logs_hot (request_type)
    WHERE request_type <> 'main';

-- 2. View freeze: append request_type to request_logs_with_current_month
DO $$
DECLARE
  new_cols text[] := ARRAY['request_type'];
  base_cols text;
  final_cols text;
  missing_count int;
  view_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF view_exists THEN
    SELECT count(*) INTO missing_count
      FROM unnest(new_cols) nc
     WHERE NOT EXISTS (
       SELECT 1
         FROM information_schema.columns
        WHERE table_name = 'request_logs_with_current_month'
          AND column_name = nc
     );

    IF missing_count = 0 THEN
      RAISE NOTICE '510: VIEW already exposes request_type — skip recreate';
      RETURN;
    END IF;

    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
     WHERE c.relname = 'request_logs_with_current_month'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '510: existing VIEW has no columns';
    END IF;
    final_cols := base_cols || ', ' || array_to_string(new_cols, ', ');
  ELSE
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
       AND h.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '510: cannot build fallback column list';
    END IF;
    final_cols := base_cols || ', ' || array_to_string(new_cols, ', ');
  END IF;

  EXECUTE 'DROP VIEW IF EXISTS request_logs_with_current_month';
  EXECUTE format($sql$
    CREATE VIEW request_logs_with_current_month AS
    SELECT %s FROM request_logs_hot
    UNION ALL
    SELECT %s FROM request_logs
  $sql$, final_cols, final_cols);

  COMMENT ON VIEW request_logs_with_current_month IS
    'Hot + monthly partitions UNION. Recreated by migration 510 (2026-08-13) '
    'to expose request_type (V3.2 主从请求分类). '
    'Preserves prior VIEW column set to avoid hot/parent type drift (see 448/459/491).';

  IF NOT EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_name = 'request_logs_with_current_month'
       AND column_name = 'request_type'
  ) THEN
    RAISE EXCEPTION '510: VIEW missing column request_type after recreate';
  END IF;

  PERFORM request_type FROM request_logs_with_current_month LIMIT 1;

  RAISE NOTICE 'Migration 510 completed: request_type on hot/parent + VIEW';
END $$;

COMMIT;

-- POST_CONDITION:
--   SELECT column_name FROM information_schema.columns
--    WHERE table_name IN ('request_logs_hot','request_logs','request_logs_with_current_month')
--      AND column_name = 'request_type';
