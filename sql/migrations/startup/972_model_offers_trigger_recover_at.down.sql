-- Migration: 972_model_offers_trigger_recover_at (down)
-- Purpose: 回滚 model_offers_update_trigger 的 unavailable_recover_at 修复。
-- Author: System
-- Date: 2026-11-17
--
-- ⚠️⚠️ **回滚 = 把已修的根因重新打开** ⚠️⚠️
--
--   本 down 件把触发器体还原成 568 的形态（SET 清单里没有 unavailable_recover_at）
--   ⇒ 经 model_offers 视图的「置可用」写入会再次**只清 reason/unavailable_at、
--   把 recover_at 留下** ⇒ F3 残留地平线会重新开始反弹（37~231 行锯齿）。
--
--   ⇒ **只有在你确认「残留反弹」比「触发器缺列」更可接受时才回滚。**
--     正常路径是**不回滚**：972 的触发器修复是纯增量（多一列的三分支 CASE，
--     分支③ 对现有调用方零行为变化），没有需要回滚的副作用。
--
-- ★ 存量清理**不可逆**：5.2 的 UPDATE 把已残留行的 recover_at 清成了 NULL，
--   原值（节点当初判不可用时写的「计划重试时刻」）**无法复原**。
--   但也不需要复原：那些行 available=TRUE（现在可路由），
--   recover_at 指向的「计划重试时刻」在可用态下本就没有语义
--   ⇒ 清掉它们是**修正**而非**破坏**。
--
-- ============================================================================
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- 还原为 568_credential_priority_flag.sql 的触发器体（逐字，即「缺列」形态）。
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
        priority = COALESCE(NEW.priority, credential_model_bindings.priority),
        updated_at = now()
    WHERE id = OLD.id;

    RETURN NEW;
END;
$$;

-- ⚠️ 不回滚存量清理（不可逆，且清掉的是矛盾态，见文件头说明）。

-- 台账自登记：down 件不删 schema_migrations 行（删除会让「已做过/未做过」失真；
-- 本仓约定是 up/down 成对投递，账本记 up 的事实）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('972', 'model_offers_update_trigger: 已回滚（⚠️ 重新打开 F3 残留反弹的根因）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
