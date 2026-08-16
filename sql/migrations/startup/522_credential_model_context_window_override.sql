-- Migration 522: credential_model_bindings.context_window_override
--
-- 每个供应商凭据下的模型上下文大小不一定与主流（标准目录 models_canonical
-- .context_window）一致：同一标准模型在不同凭证、不同套餐/上游端点下的实际上下文
-- 窗口可能被供应商上修或下修（虚标）。469 已在 models_canonical 上增加了
-- context_window_override，但那是「一个标准模型一个值」，所有映射到该标准模型
-- 的凭据共享同一窗口。本迁移把覆盖能力下沉到 credential_model_bindings —— 即
-- 「凭据 × 模型」粒度，让运营可以针对某一条凭证下的某一个模型单独标定上下文
-- 大小，而不影响其它凭据下同名的模型。
--
-- 新增列（均与 469 同语义，可空、可回滚）：
--   context_window_override  integer  —— 非空时优先于 models_canonical 的值
--   context_window_source    text     —— provenance: catalog/discovery/manual/probe
--   context_window_updated_at timestamptz —— 最近一次覆盖时间（审计）
--
-- 运行时优先级链（provider/client.go 候选 SQL）：
--   COALESCE(cmb.context_window_override, mc.context_window_override, mc.context_window)
--   即：凭据级覆盖 > 标准模型级覆盖 > 标准目录默认值
--
-- model_offers 视图新增 context_window_override 列，便于读路径与候选 SQL 通过
-- mo.context_window_override 取值；INSTEAD OF UPDATE 触发器以 COALESCE 透传，
-- 避免视图级部分更新静默丢弃该列。
--
-- Idempotent: IF NOT EXISTS 守卫，可重复执行。

BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'credential_model_bindings'
      AND column_name = 'context_window_override'
  ) THEN
    ALTER TABLE public.credential_model_bindings ADD COLUMN context_window_override integer;
    COMMENT ON COLUMN public.credential_model_bindings.context_window_override IS '凭据×模型级上下文窗口覆盖。非空时优先于 models_canonical.context_window_override / context_window。';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'credential_model_bindings'
      AND column_name = 'context_window_source'
  ) THEN
    ALTER TABLE public.credential_model_bindings ADD COLUMN context_window_source text DEFAULT 'catalog';
    COMMENT ON COLUMN public.credential_model_bindings.context_window_source IS '凭据×模型级上下文窗口的 provenance：catalog/discovery/manual/probe。';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'credential_model_bindings'
      AND column_name = 'context_window_updated_at'
  ) THEN
    ALTER TABLE public.credential_model_bindings ADD COLUMN context_window_updated_at timestamp with time zone;
    COMMENT ON COLUMN public.credential_model_bindings.context_window_updated_at IS '凭据×模型级 context_window_override 的最近一次更新时间。';
  END IF;
END $$;

-- 暴露凭据级覆盖给读路径与候选 SQL（mo.context_window_override）。
CREATE OR REPLACE VIEW public.model_offers AS
 SELECT cmb.id,
    cmb.credential_id,
    pm.canonical_id,
    pm.canonical_raw_name,
    pm.raw_model_name,
    cmb.success_rate,
    cmb.p95_latency_ms,
    cmb.available,
    pm.last_seen_at,
    cmb.routing_tier,
    cmb.weight,
    cmb.unit_price_in_per_1m,
    cmb.unit_price_out_per_1m,
    cmb.currency,
    pm.outbound_model_name,
    cmb.cache_read_price_per_1m,
    cmb.cache_write_price_per_1m,
    pm.standardized_name,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.unavailable_recover_at,
    cmb.billing_mode,
    cmb.pricing_source,
    cmb.pricing_updated_at,
    cmb.manual_priority,
    cmb.active_sessions,
    cmb.consecutive_failures,
    cmb.admin_protected,
    cmb.created_at,
    cmb.updated_at,
    pm.modality AS provider_modality,
    cmb.context_window_override
   FROM (public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));

-- 让 INSTEAD OF UPDATE 触发器透传新列，避免视图级部分更新静默丢弃该列。
CREATE OR REPLACE FUNCTION public.model_offers_update_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_pm_id BIGINT;
BEGIN
    SELECT provider_model_id INTO v_pm_id
    FROM credential_model_bindings WHERE id = OLD.id;

    IF v_pm_id IS NOT NULL THEN
        UPDATE provider_models SET
            canonical_id = COALESCE(NEW.canonical_id, provider_models.canonical_id),
            standardized_name = COALESCE(NEW.standardized_name, provider_models.standardized_name),
            outbound_model_name = COALESCE(NEW.outbound_model_name, provider_models.outbound_model_name),
            last_seen_at = COALESCE(NEW.last_seen_at, provider_models.last_seen_at),
            updated_at = now()
        WHERE id = v_pm_id;
    END IF;

    UPDATE credential_model_bindings SET
        available = COALESCE(NEW.available, credential_model_bindings.available),
        unavailable_reason = CASE
            WHEN NEW.unavailable_reason IS NOT NULL THEN NEW.unavailable_reason
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_reason
        END,
        unavailable_at = CASE
            WHEN NEW.unavailable_at IS NOT NULL THEN NEW.unavailable_at
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_at
        END,
        admin_protected = CASE
            WHEN NEW.admin_protected IS NOT NULL THEN NEW.admin_protected
            ELSE credential_model_bindings.admin_protected
        END,
        routing_tier = COALESCE(NEW.routing_tier, credential_model_bindings.routing_tier),
        weight = COALESCE(NEW.weight, credential_model_bindings.weight),
        manual_priority = COALESCE(NEW.manual_priority, credential_model_bindings.manual_priority),
        success_rate = COALESCE(NEW.success_rate, credential_model_bindings.success_rate),
        p95_latency_ms = COALESCE(NEW.p95_latency_ms, credential_model_bindings.p95_latency_ms),
        active_sessions = COALESCE(NEW.active_sessions, credential_model_bindings.active_sessions),
        consecutive_failures = COALESCE(NEW.consecutive_failures, credential_model_bindings.consecutive_failures),
        unit_price_in_per_1m = COALESCE(NEW.unit_price_in_per_1m, credential_model_bindings.unit_price_in_per_1m),
        unit_price_out_per_1m = COALESCE(NEW.unit_price_out_per_1m, credential_model_bindings.unit_price_out_per_1m),
        cache_read_price_per_1m = COALESCE(NEW.cache_read_price_per_1m, credential_model_bindings.cache_read_price_per_1m),
        cache_write_price_per_1m = COALESCE(NEW.cache_write_price_per_1m, credential_model_bindings.cache_write_price_per_1m),
        currency = COALESCE(NEW.currency, credential_model_bindings.currency),
        billing_mode = COALESCE(NEW.billing_mode, credential_model_bindings.billing_mode),
        pricing_source = COALESCE(NEW.pricing_source, credential_model_bindings.pricing_source),
        pricing_updated_at = COALESCE(NEW.pricing_updated_at, credential_model_bindings.pricing_updated_at),
        context_window_override = COALESCE(NEW.context_window_override, credential_model_bindings.context_window_override),
        updated_at = now()
    WHERE id = OLD.id;

    RETURN NEW;
END;
$$;

COMMIT;
