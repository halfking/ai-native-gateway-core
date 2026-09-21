-- Migration 678: 修复 request_logs_bodies_hot UNIQUE 约束 + 补全 model_offers 视图列
--
-- 原编号 676,与已存在的 676_routing_opt_active_fix.sql 撞号;
-- 2026-09-07 改为 678,遵循 TestNumericUpMigrationVersionsAreUnique 契约。
--
-- 2026-09-07 PG log audit:
--   1) request_logs_bodies_hot 的 UNIQUE 索引实际仍是 (request_id, ts),
--      但 Go 代码 upsertRequestLogBodies (client.go:2498) 使用
--      `ON CONFLICT (request_id)`,所以每个写入都触发
--      `there is no unique or exclusion constraint matching the ON CONFLICT
--       specification` (SQLSTATE 42P10),回滚整个 telemetry 事务,降级到
--      Redis fallback,memory 段丢失 body 写入。
--      migration 455 已声明完成该修复但 PG 实例上 unique 索引没被替换
--      (request_logs_hot 已成功切到 (request_id) PK; bodies_hot 仍为
--      (request_id, ts) 复合索引)。本迁移独立完成该步骤。
--
--   2) model_offers 视图不含 `priority` 与 `unavailable_recover_at` 列,
--      但 provider/client.go:1497 用 `mo.priority`(本迁移同时修 Go 侧
--      `mo.priority` -> `mo.manual_priority`),bg/credential_recovery.go
--      偶发 UPDATE model_offers SET unavailable_recover_at 也触发 42703。
--      在视图上补出两列,既消除运行时 42703,又避免改 N 处 Go 调用点。
--
-- 幂等:每个 ALTER/DROP/CREATE 都用 IF EXISTS / IF NOT EXISTS 守护。

BEGIN;

-- ============================================================
-- A. request_logs_bodies_hot: 拆掉 (request_id, ts) 复合 unique,
--    重建为 (request_id) 唯一索引(对齐 Go 代码 ON CONFLICT (request_id))
-- ============================================================

DO $$
DECLARE
  idx_exists boolean;
  dup_count bigint;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE indexname = 'idx_request_logs_bodies_hot_request_id_ts_unique'
    AND tablename = 'request_logs_bodies_hot'
  ) INTO idx_exists;

  IF NOT idx_exists THEN
    RAISE NOTICE 'idx_request_logs_bodies_hot_request_id_ts_unique already removed; skipping rebuild';
    RETURN;
  END IF;

  -- 去重:每个 request_id 保留 ts 最新行
  WITH dupes AS (
    SELECT request_id
    FROM request_logs_bodies_hot
    GROUP BY request_id
    HAVING COUNT(*) > 1
  )
  SELECT COUNT(*) INTO dup_count FROM dupes;
  RAISE NOTICE 'request_logs_bodies_hot duplicates before dedupe: %', dup_count;

  DELETE FROM request_logs_bodies_hot
  WHERE (request_id, ts) IN (
    SELECT request_id, ts FROM (
      SELECT request_id, ts,
        ROW_NUMBER() OVER (PARTITION BY request_id ORDER BY ts DESC) AS rn
      FROM request_logs_bodies_hot
    ) sub
    WHERE rn > 1
  );

  DROP INDEX IF EXISTS idx_request_logs_bodies_hot_request_id_ts_unique;
  CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_bodies_hot_request_id
    ON request_logs_bodies_hot (request_id);

  RAISE NOTICE 'rebuilt request_logs_bodies_hot UNIQUE on (request_id)';
END $$;

-- ============================================================
-- B. model_offers 视图补 priority / unavailable_recover_at 列
--    视图 = cmb JOIN pm;priority 取 manual_priority(避免误导);unavailable_recover_at
--    来自 cmb,内容是冷却恢复时刻(bg/credential_recovery 直接 UPDATE cmb 即可,
--    但 mnf_cooling mirror 等代码历史上写过视图,补列以保持兼容)。
-- ============================================================

-- 2026-09-07 (678): model_offers 是带 INSTEAD OF 触发器的视图,无法
-- ALTER VIEW ADD COLUMN;走 DROP VIEW + 重建视图 + 重建触发器。三个
-- INSTEAD OF 触发器函数本身引用具体表字段,不受视图列顺序影响。
DO $$
DECLARE
  v_insert_trg text;
  v_update_trg text;
  v_delete_trg text;
  v_insert_fn text;
  v_update_fn text;
  v_delete_fn text;
BEGIN
  -- 保存三个触发器的 CREATE TRIGGER 语句,稍后原样重建
  SELECT pg_get_triggerdef(oid) INTO v_insert_trg FROM pg_trigger
    WHERE tgrelid='model_offers'::regclass AND tgname='model_offers_insert';
  SELECT pg_get_triggerdef(oid) INTO v_update_trg FROM pg_trigger
    WHERE tgrelid='model_offers'::regclass AND tgname='model_offers_update';
  SELECT pg_get_triggerdef(oid) INTO v_delete_trg FROM pg_trigger
    WHERE tgrelid='model_offers'::regclass AND tgname='model_offers_delete';

  -- 取三个触发器函数(由模型提供,这里只重建 trigger,函数不动)
  SELECT p.proname INTO v_insert_fn FROM pg_proc p
    JOIN pg_trigger t ON t.tgfoid = p.oid
    WHERE t.tgrelid='model_offers'::regclass AND t.tgname='model_offers_insert';
  SELECT p.proname INTO v_update_fn FROM pg_proc p
    JOIN pg_trigger t ON t.tgfoid = p.oid
    WHERE t.tgrelid='model_offers'::regclass AND t.tgname='model_offers_update';
  SELECT p.proname INTO v_delete_fn FROM pg_proc p
    JOIN pg_trigger t ON t.tgfoid = p.oid
    WHERE t.tgrelid='model_offers'::regclass AND t.tgname='model_offers_delete';

  -- 删除触发器(DROP VIEW 会 CASCADE 删除触发器;但函数属于 schema 范围保留)
  DROP TRIGGER IF EXISTS model_offers_insert ON public.model_offers;
  DROP TRIGGER IF EXISTS model_offers_update ON public.model_offers;
  DROP TRIGGER IF EXISTS model_offers_delete ON public.model_offers;

  -- 重建视图,在末尾追加 priority (manual_priority<>0) 与 unavailable_recover_at
  -- DROP VIEW + CREATE VIEW(不能用 CREATE OR REPLACE:PostgreSQL 会严格按
  -- attname 位置对应,新增列即报 "cannot change name of view column")
  EXECUTE 'DROP VIEW public.model_offers';
  EXECUTE $v$
    CREATE VIEW public.model_offers AS
    SELECT cmb.id, cmb.credential_id, pm.canonical_id, pm.raw_model_name,
           pm.canonical_raw_name, cmb.success_rate, cmb.p95_latency_ms,
           cmb.available, pm.last_seen_at, cmb.routing_tier, cmb.weight,
           cmb.unit_price_in_per_1m, cmb.unit_price_out_per_1m, cmb.currency,
           pm.outbound_model_name, cmb.cache_read_price_per_1m,
           cmb.cache_write_price_per_1m, pm.standardized_name,
           cmb.unavailable_reason, cmb.unavailable_at, cmb.billing_mode,
           cmb.pricing_source, cmb.pricing_updated_at, cmb.manual_priority,
           (cmb.manual_priority <> 0) AS priority,
           cmb.unavailable_recover_at,
           cmb.active_sessions, cmb.consecutive_failures, cmb.admin_protected
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
  $v$;

  -- 重建三个 INSTEAD OF 触发器(复用原函数定义,触发器属性从保存的 trg 文案中复用)
  IF v_insert_trg IS NOT NULL THEN EXECUTE v_insert_trg; END IF;
  IF v_update_trg IS NOT NULL THEN EXECUTE v_update_trg; END IF;
  IF v_delete_trg IS NOT NULL THEN EXECUTE v_delete_trg; END IF;

  RAISE NOTICE 'rebuilt model_offers view with priority + unavailable_recover_at columns; triggers reattached';
END
$$;

COMMIT;