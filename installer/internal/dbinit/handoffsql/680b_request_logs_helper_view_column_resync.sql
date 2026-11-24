-- 680b: rebuild the request_logs_with_current_month wrapper chain from the
--       live catalog (host-first cold-start resync)
--
-- 日期: 2026-10-11
--
-- Purpose
-- ───────
-- canonical 月视图族是三层包装：
--   without_customer_id            = hot∩parent 交集（去掉 customer_id /
--                                    request_class / due_at 三个后追加列）
--   without_request_class_due_at   = 上层 + customer_id（hot-first lateral）
--   request_logs_with_current_month= 上层 + request_class/due_at（734 重建为
--                                    v3：details JOIN 形态）
-- 各层只在"缺席"时被 680 引导分支动态重建；已存在的层永远保留建层时刻的
-- 列集。host-first 部署的库上，各层首次建成时 491（t0–t9）/ 532
-- （is_final_success）/ 487 族（system_fingerprint 等）尚未登记应用，层列集
-- 陈旧——734 读该族 42703（本机实测 'column "t0_arrived_at" does not
-- exist' 与 'column "is_final_success" does not exist'），重放 610/680 也
-- 不修（各自的"已扩展"早退分支），且 CREATE OR REPLACE VIEW 不允许增删列。
--
-- 本迁移按 680 引导分支的同一规则从活动 catalog 重建下两层（CREATE OR
-- REPLACE 不行，必须 DROP CASCADE；canonical 随 CASCADE 丢弃后由紧随其后
-- 的 734 立即按 v3 最终形态重建）。交集规则与 680 / db/
-- request_logs_view_schema.go 逐字一致：hot∩parent，排除后追加列，HOT_ONLY
-- 列（caller_id / session_correlation_id，见 db/
-- request_logs_view_schema_test.go"must never leak into the UNION"）天然
-- 排除。健康库上重建前后列集相同，零变化。
--
-- Idempotent: YES（列集落后才重建；重建后再次运行零变化）
-- Breaking: NO（canonical 由 734/启动 ensure 链即刻恢复）

BEGIN;

DO $$
DECLARE
  base_cols text;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'public' AND c.relname = 'request_logs_hot'
  ) THEN
    -- 冷启动（表族尚未建）：整个视图族由 680/710/734 与启动 ensure 链负责。
    RETURN;
  END IF;

  SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
    INTO base_cols
    FROM pg_attribute h
    JOIN pg_class hc ON hc.oid = h.attrelid
    JOIN pg_namespace hn ON hn.oid = hc.relnamespace
    JOIN pg_attribute p ON p.attrelid = ('public.request_logs')::regclass
                       AND p.attname = h.attname
    JOIN pg_class pc ON pc.oid = p.attrelid
    JOIN pg_namespace pn ON pn.oid = pc.relnamespace
   WHERE hn.nspname = 'public'
     AND hc.relname = 'request_logs_hot'
     AND pn.nspname = 'public'
     AND pc.relname = 'request_logs'
     AND h.attnum > 0 AND NOT h.attisdropped
     AND p.attnum > 0 AND NOT p.attisdropped
     AND h.attname NOT IN ('customer_id', 'request_class', 'due_at');

  IF base_cols IS NULL OR base_cols = '' THEN
    RAISE EXCEPTION '680b: cannot derive base column list for request_logs view chain';
  END IF;

  -- Drop order mirrors 680's rebuild branch: the canonical may be an old
  -- monolithic shape (e.g. the 01-schema.sql snapshot's pre-577 body) that
  -- references hot/parent directly and is NOT touched by the wrapper
  -- CASCADEs below — drop it explicitly, wrappers first-dependency or not.
  -- CREATE OR REPLACE VIEW cannot add/remove columns, so a stale-shape
  -- canonical must go; 734 (next in the sequence) recreates it as v3.
  EXECUTE 'DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE';
  EXECUTE 'DROP VIEW IF EXISTS public.request_logs_with_current_month_without_request_class_due_at CASCADE';
  EXECUTE 'DROP VIEW IF EXISTS public.request_logs_with_current_month_without_customer_id CASCADE';
  EXECUTE format(
    'CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
     SELECT %1$s FROM public.request_logs_hot
     UNION ALL
     SELECT %1$s FROM public.request_logs',
    base_cols
  );

  -- Migration 577 stage: append customer_id via hot-first lateral lookup.
  CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
  SELECT v.*, m.customer_id
  FROM public.request_logs_with_current_month_without_customer_id v
  LEFT JOIN LATERAL (
      SELECT customer_id FROM public.request_logs_hot
      WHERE request_id = v.request_id AND ts = v.ts
      UNION ALL
      SELECT customer_id FROM public.request_logs
      WHERE request_id = v.request_id AND ts = v.ts
      LIMIT 1
  ) m ON true;
END $$;

COMMIT;
