-- Migration 682: model_offers 视图补 context_window_override / _source / _updated_at 列
--
-- Background (2026-09-07 网关日志审计, 42703 x 92+):
--   provider/client.go:1507 的候选查询引用
--     COALESCE(mo.context_window_override, mc.context_window_override, mc.context_window)
--   其中 mo = model_offers 视图。469 给 models_canonical (mc.*) 加了三列,
--   credential_model_bindings(视图的另一数据源,cmb.*)也持有同款三列,
--   但视图本身从未透出 context_window_override —— 每个 routing 候选查询
--   42703 "column mo.context_window_override does not exist"。
--   与 678 (priority / unavailable_recover_at) 完全同类:模型列加了,
--   视图没跟着重建。
--
-- Fix: 按 678 的模式 DROP VIEW + 重建(末尾追加三列, 源 = provider_models)
--   + 重挂三个 INSTEAD OF 触发器。触发器函数引用具体表字段,不受视图
--   列顺序影响,原样保存/重建即可。
--
-- Idempotent: YES(IF NOT EXISTS 前置检查 + DROP/CREATE 幂等)。

DO $$
DECLARE
  v_has_col boolean;
  v_insert_trg text;
  v_update_trg text;
  v_delete_trg text;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'model_offers'
      AND column_name = 'context_window_override'
  ) INTO v_has_col;
  IF v_has_col THEN
    RAISE NOTICE 'model_offers already exposes context_window_override; skipping';
    RETURN;
  END IF;

  -- to_regclass 为空说明视图尚不存在(全新库由更早迁移创建),无可修复
  IF to_regclass('public.model_offers') IS NULL THEN
    RAISE NOTICE 'model_offers view does not exist; skipping';
    RETURN;
  END IF;

  -- 保存三个 INSTEAD OF 触发器的 CREATE TRIGGER 语句,稍后原样重建
  SELECT pg_get_triggerdef(oid) INTO v_insert_trg FROM pg_trigger
    WHERE tgrelid='model_offers'::regclass AND tgname='model_offers_insert';
  SELECT pg_get_triggerdef(oid) INTO v_update_trg FROM pg_trigger
    WHERE tgrelid='model_offers'::regclass AND tgname='model_offers_update';
  SELECT pg_get_triggerdef(oid) INTO v_delete_trg FROM pg_trigger
    WHERE tgrelid='model_offers'::regclass AND tgname='model_offers_delete';

  DROP TRIGGER IF EXISTS model_offers_insert ON public.model_offers;
  DROP TRIGGER IF EXISTS model_offers_update ON public.model_offers;
  DROP TRIGGER IF EXISTS model_offers_delete ON public.model_offers;

  -- 不能 CREATE OR REPLACE:PG 按列名+位置严格校验,追加列直接报
  -- "cannot change name of view column"。列顺序与 678 重建版完全一致,
  -- 末尾追加三列。源 = credential_model_bindings(cmb):该表持有
  -- context_window_override/_source/_updated_at(469 同款,凭据×模型级
  -- 手动校准),与 provider/client.go "522" 注释的优先级第一层
  -- 「凭据×模型级覆盖」语义一致;provider_models 上并无这三列。
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
           cmb.active_sessions, cmb.consecutive_failures, cmb.admin_protected,
           cmb.context_window_override,
           cmb.context_window_source,
           cmb.context_window_updated_at
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
  $v$;

  IF v_insert_trg IS NOT NULL THEN EXECUTE v_insert_trg; END IF;
  IF v_update_trg IS NOT NULL THEN EXECUTE v_update_trg; END IF;
  IF v_delete_trg IS NOT NULL THEN EXECUTE v_delete_trg; END IF;

  RAISE NOTICE 'rebuilt model_offers view with context_window_override/_source/_updated_at; triggers reattached';
END
$$;
