-- Migration 532: Session-level "single final success" marker on request_logs*
--
-- 日期: 2026-08-18
--
-- Purpose
-- ───────
-- 会话优化 v4 T7 / R6.2 / P1-7：一个 gw_session_id 至多一条「最终成功」记录。
-- 客户端对同一会话的重发（migration 054 场景：5 次重发曾产生 5 行全 success）
-- 在库层面无唯一性约束，管理端/账本无法区分哪一行是客户端真正收到的成功。
--
-- 本迁移增加行级最终成功标记 + 会话级部分唯一索引（唯一性只约束被标记的行，
-- 空/NULL gw_session_id 的行不受约束）：
--
--   is_final_success BOOLEAN NOT NULL DEFAULT FALSE
--
-- 写入侧（domains/hooks/observability/telemetry/client.go）在成功终态落库的
-- 同一事务内做 claim（NOT EXISTS 检查 + 本索引兜底），并发双 claim 的一方
-- 捕获 23505 后降级为普通成功（保留 success，不回改历史行）。
-- 失败/取消/空 gw_session_id 路径不参与 claim。
--
-- Tables:
--   request_logs_hot  — write path (telemetry INSERT/UPDATE)
--   request_logs      — parent partitioned table (promote SELECT * requires parity)
--   request_logs_*    — existing monthly partitions (per-partition index)
--
-- Index placement（分区约束依据）:
--   request_logs 是声明式分区母表（PARTITION BY RANGE (ts)，见 01-schema.sql
--   / ensure_request_logs_partition）。PostgreSQL 要求分区母表上的 UNIQUE
--   索引必须包含全部分区键（SQLSTATE 42P17 "insufficient columns in UNIQUE
--   constraint definition"），(gw_session_id) 单列无法在母表建唯一索引。
--   已存档分区还是 columnar 存取方法（01-schema.sql 在 request_logs_2026_07/08
--   的 CREATE TABLE 前 SET default_table_access_method = columnar；migration 399
--   注释 "request_logs (ALL columnar)"），columnar 分区上唯一索引可能被拒绝。
--   仓库既有先例（341/455 热表独立化）也是只在 hot 侧建唯一索引。
--   因此：
--     1) request_logs_hot（heap 热表，非分区）— 写路径的硬保证（唯一索引）
--     2) 每个已存在的月度分区 — best-effort 唯一索引（DO 循环 + 异常降级：
--        columnar/其它 AM 拒绝时 RAISE WARNING 跳过，绝不阻塞升级）
--     3) ensure_request_logs_partition 重建后为新分区尽力创建（同样带异常降级）
--   分区侧索引缺失不破坏语义：写入侧 claim 的 NOT EXISTS 同时检查 hot 与
--   母表（覆盖跨 hot/分区边界的 >7 天重发窗口），hot 唯一索引兜底并发双写。
--
-- 索引创建安全性：部分索引谓词 WHERE is_final_success 要求列为 TRUE，而存量
-- 行全部默认 FALSE，即使历史数据存在同会话多行 success（054 缺陷数据），
-- 索引构建也不会命中唯一冲突；历史数据修正走只读报告
-- sql/scripts/report_duplicate_session_success.sql（不改数）。
--
-- View freeze (rule 49 §9.2 / rule 38 §5.1 2b):
--   Rebuild request_logs_with_current_month using the 448/459/491/510 append
--   pattern so admin SELECTs that project is_final_success do not 42703.
--
-- Idempotent: YES (IF NOT EXISTS + DO $$ + view early-return)
-- Down: 532_request_logs_final_success.down.sql
-- Breaking: NO (有默认值 FALSE，存量行不受影响)

BEGIN;

-- 1. Column on hot + parent (appended at the same tail position → promote
--    INSERT INTO request_logs SELECT * FROM request_logs_hot stays aligned,
--    same argument as migrations 491/510).
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN request_logs_hot.is_final_success IS
  'v4 T7 (2026-08-18): 会话级唯一最终成功标记。同一 gw_session_id 至多一行为 TRUE '
  '(uq_request_logs_hot_final_success_session 部分唯一索引)。客户端重发产生的其余 '
  'success 行保持 FALSE（读路径按「被取代成功」标注），失败/取消行恒为 FALSE。';
COMMENT ON COLUMN request_logs.is_final_success IS
  'v4 T7 (2026-08-18): 会话级唯一最终成功标记（promote 分区侧，与 request_logs_hot 同义）。'
  '母表因分区键限制、columnar 分区因 AM 限制无法建全局唯一索引，分区侧唯一性由写入侧 '
  'claim 的 NOT EXISTS 检查（hot + 母表）保证，heap 分区上尽力维护 uq_<partition>_final_success_session。';

-- 2. Partial unique index on the hot write path (THE hard guarantee).
CREATE UNIQUE INDEX IF NOT EXISTS uq_request_logs_hot_final_success_session
    ON request_logs_hot (gw_session_id)
    WHERE is_final_success
      AND gw_session_id IS NOT NULL
      AND gw_session_id <> '';

-- 3. Best-effort partial unique index on every existing monthly partition.
--    Partitioned parent cannot carry it (partition key ts not in the key set);
--    columnar partitions may reject unique indexes — degrade to WARNING.
DO $$
DECLARE
  part record;
BEGIN
  FOR part IN
    SELECT c.relname AS partition_name
      FROM pg_inherits i
      JOIN pg_class parent ON parent.oid = i.inhparent
      JOIN pg_class c ON c.oid = i.inhrelid
     WHERE parent.relname = 'request_logs'
       AND parent.relnamespace = 'public'::regnamespace
       AND c.relkind = 'r'
  LOOP
    BEGIN
      EXECUTE format(
        'CREATE UNIQUE INDEX IF NOT EXISTS uq_%s_final_success_session ON %I (gw_session_id) WHERE is_final_success AND gw_session_id IS NOT NULL AND gw_session_id <> ''''',
        part.partition_name, part.partition_name
      );
      RAISE NOTICE '532: partial unique final-success index ensured on %', part.partition_name;
    EXCEPTION WHEN OTHERS THEN
      RAISE WARNING '532: final-success index skipped on % (likely columnar AM): %', part.partition_name, SQLERRM;
    END;
  END LOOP;
END $$;

-- 4. ensure_request_logs_partition: extended so FUTURE monthly partitions get
--    the same partial unique index at creation time (CREATE INDEX IF NOT
--    EXISTS keeps re-runs idempotent even if the partition was just created
--    by an older copy of this function).
CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date := date_trunc('month', target_ts)::date;
    month_end     date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name     text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-06-24 (migration 043): GIN trgm on client_model so the
        -- /api/logs ?model= ILIKE filter can use a bitmap index scan
        -- instead of a partition Seq Scan once volume grows.
        EXECUTE format(
            'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-08-18 (migration 532): session-level single final-success
        -- guarantee on the promoted (archived) side, best-effort. Parent-table
        -- UNIQUE is impossible without the partition key (ts); columnar AM
        -- partitions may reject unique indexes — degrade to WARNING so the
        -- promote path never breaks. Hot-side unique index is the hard guard.
        BEGIN
          EXECUTE format(
            'CREATE UNIQUE INDEX IF NOT EXISTS uq_%s_final_success_session ON %I (gw_session_id) WHERE is_final_success AND gw_session_id IS NOT NULL AND gw_session_id <> ''''',
            part_name, part_name
          );
        EXCEPTION WHEN OTHERS THEN
          RAISE WARNING 'ensure_request_logs_partition: final-success index skipped on % (%)', part_name, SQLERRM;
        END;
    END IF;
END;
$$;

-- 5. View freeze: append is_final_success to request_logs_with_current_month
--    (verbatim 510 append pattern).
DO $$
DECLARE
  new_cols text[] := ARRAY['is_final_success'];
  base_cols text;
  final_cols text;
  missing_count int;
  view_exists boolean;
BEGIN
  -- Catalog lookups are schema-qualified to public: an unqualified relname
  -- match can hit a same-named object in another schema and wrongly skip the
  -- rebuild (observed on scratch databases carrying fixture schemas).
  SELECT EXISTS (
    SELECT 1 FROM pg_views
     WHERE viewname = 'request_logs_with_current_month'
       AND schemaname = 'public'
  ) INTO view_exists;

  IF view_exists THEN
    SELECT count(*) INTO missing_count
      FROM unnest(new_cols) nc
     WHERE NOT EXISTS (
       SELECT 1
         FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'request_logs_with_current_month'
          AND column_name = nc
     );

    IF missing_count = 0 THEN
      RAISE NOTICE '532: VIEW already exposes is_final_success — skip recreate';
      RETURN;
    END IF;

    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname = 'request_logs_with_current_month'
       AND c.relkind = 'v'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '532: existing VIEW has no columns';
    END IF;
    final_cols := base_cols || ', ' || array_to_string(new_cols, ', ');
  ELSE
    SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
      INTO base_cols
      FROM pg_attribute h
      JOIN pg_class ch ON h.attrelid = ch.oid
      JOIN pg_namespace nh ON nh.oid = ch.relnamespace
      JOIN pg_attribute p ON p.attname = h.attname
      JOIN pg_class cp ON p.attrelid = cp.oid
      JOIN pg_namespace np ON np.oid = cp.relnamespace
     WHERE nh.nspname = 'public' AND ch.relname = 'request_logs_hot'
       AND np.nspname = 'public' AND cp.relname = 'request_logs'
       AND h.attnum > 0 AND NOT h.attisdropped
       AND p.attnum > 0 AND NOT p.attisdropped
       AND h.atttypid = p.atttypid
       AND h.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '532: cannot build fallback column list';
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
    'Hot + monthly partitions UNION. Recreated by migration 532 (2026-08-18) '
    'to expose is_final_success (v4 T7 session-level single final-success). '
    'Preserves prior VIEW column set to avoid hot/parent type drift (see 448/459/491/510).';

  IF NOT EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'request_logs_with_current_month'
       AND column_name = 'is_final_success'
  ) THEN
    RAISE EXCEPTION '532: VIEW missing column is_final_success after recreate';
  END IF;

  PERFORM is_final_success FROM request_logs_with_current_month LIMIT 1;

  RAISE NOTICE 'Migration 532 completed: is_final_success on hot/parent/partitions + unique indexes + VIEW';
END $$;

COMMIT;

-- POST_CONDITION:
--   SELECT table_name, column_name FROM information_schema.columns
--    WHERE column_name = 'is_final_success'
--      AND table_name IN ('request_logs_hot','request_logs','request_logs_with_current_month');
--   SELECT indexname FROM pg_indexes
--    WHERE indexname LIKE '%final_success_session%';
