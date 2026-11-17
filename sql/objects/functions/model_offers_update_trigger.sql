--
-- Name: model_offers_update_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_update_trigger() RETURNS trigger
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
        -- ★ 972（2026-11-17 F3 残留地平线根修轮）：补第四列 unavailable_recover_at。
        --   此前触发器的 SET 清单里没有这一列 ⇒ 经 model_offers 视图的「置可用」
        --   写入只清 reason/unavailable_at、把恢复标记留下 ⇒ 行落进矛盾态
        --   available=TRUE ∧ recover_at≠NULL ⇒ 节点状态恢复地平线（F3 残留）
        --   在 37~231 行之间反复反弹（2026-11-17 观测 19 轮，从未归零）。
        --   ★ 分支顺序**不能**照抄上面两列（「显式给值」在前）：INSTEAD OF UPDATE
        --   的 NEW 行里未被 SET 触碰的列携带旧值 ⇒ 调用方不写 recover_at 时
        --   NEW 仍是旧的未来时间戳 ⇒ 「显式给值」在前会保留旧标记 ⇒ 缺陷照旧。
        --   ⇒ 状态语义（available=TRUE ⇒ 无待恢复）必须优先于来源语义。
        unavailable_recover_at = CASE
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            ELSE credential_model_bindings.unavailable_recover_at
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
        -- 522: 凭据×模型级上下文窗口覆盖。COALESCE 透传保证视图级部分更新
        -- 不会静默丢弃该列；清空（置 NULL）由 admin 直接写 cmb 处理。
        context_window_override = COALESCE(NEW.context_window_override, credential_model_bindings.context_window_override),
        priority = COALESCE(NEW.priority, credential_model_bindings.priority),
        updated_at = now()
    WHERE id = OLD.id;

    RETURN NEW;
END;
$$;

