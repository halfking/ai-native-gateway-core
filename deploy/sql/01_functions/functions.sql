-- ============================================================================
-- llm-gateway-go 触发器和函数定义
-- ============================================================================

-- ----------------------------------------------------------------------------
-- 路由覆盖审计触发器函数
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION routing_overrides_audit_fn()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT := COALESCE(
        NULLIF(current_setting('app.current_admin', true), ''),
        'system'
    );
BEGIN
    IF (TG_OP = 'INSERT') THEN
        INSERT INTO routing_overrides_audit
            (action, override_id, task_type, profile, mode,
             model_chosen, reason, expires_at, actor)
        VALUES
            ('insert', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
             NEW.model_chosen, NEW.reason, NEW.expires_at, v_actor);
        RETURN NEW;
    ELSIF (TG_OP = 'UPDATE') THEN
        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at
           OR NEW.reason IS DISTINCT FROM OLD.reason
           OR NEW.model_chosen IS DISTINCT FROM OLD.model_chosen
        THEN
            INSERT INTO routing_overrides_audit
                (action, override_id, task_type, profile, mode,
                 model_chosen, reason, expires_at, old_expires_at,
                 actor)
            VALUES
                ('update', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
                 NEW.model_chosen, NEW.reason, NEW.expires_at,
                 OLD.expires_at, v_actor);
        END IF;
        RETURN NEW;
    ELSIF (TG_OP = 'DELETE') THEN
        INSERT INTO routing_overrides_audit
            (action, override_id, task_type, profile, mode,
             model_chosen, reason, expires_at, actor)
        VALUES
            ('delete', OLD.id, OLD.task_type, OLD.profile, OLD.mode,
             OLD.model_chosen, OLD.reason, OLD.expires_at, v_actor);
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS routing_overrides_audit_trg ON routing_overrides;
CREATE TRIGGER routing_overrides_audit_trg
    AFTER INSERT OR UPDATE OR DELETE ON routing_overrides
    FOR EACH ROW EXECUTE FUNCTION routing_overrides_audit_fn();

-- ----------------------------------------------------------------------------
-- 租户模型策略审计触发器函数
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION tenant_model_policies_audit_fn()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT := COALESCE(
        NULLIF(current_setting('app.current_admin', true), ''),
        'system'
    );
BEGIN
    IF (TG_OP = 'INSERT') THEN
        INSERT INTO tenant_model_policies_audit
            (action, policy_id, tenant_id, canonical_name, reason, actor)
        VALUES
            ('insert', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
        RETURN NEW;
    ELSIF (TG_OP = 'UPDATE') THEN
        IF NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
            IF NEW.deleted_at IS NULL THEN
                INSERT INTO tenant_model_policies_audit
                    (action, policy_id, tenant_id, canonical_name, reason, actor)
                VALUES
                    ('undelete', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
            ELSE
                INSERT INTO tenant_model_policies_audit
                    (action, policy_id, tenant_id, canonical_name, reason, actor)
                VALUES
                    ('delete', NEW.id, NEW.tenant_id, NEW.canonical_name, OLD.reason, v_actor);
            END IF;
        ELSIF NEW.reason IS DISTINCT FROM OLD.reason
              OR NEW.canonical_name IS DISTINCT FROM OLD.canonical_name
        THEN
            INSERT INTO tenant_model_policies_audit
                (action, policy_id, tenant_id, canonical_name, reason, actor)
            VALUES
                ('update', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
        END IF;
        RETURN NEW;
    ELSIF (TG_OP = 'DELETE') THEN
        INSERT INTO tenant_model_policies_audit
            (action, policy_id, tenant_id, canonical_name, reason, actor)
        VALUES
            ('delete', OLD.id, OLD.tenant_id, OLD.canonical_name, OLD.reason, v_actor);
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS tenant_model_policies_audit_trg ON tenant_model_policies;
CREATE TRIGGER tenant_model_policies_audit_trg
    AFTER INSERT OR UPDATE OR DELETE ON tenant_model_policies
    FOR EACH ROW EXECUTE FUNCTION tenant_model_policies_audit_fn();

-- ----------------------------------------------------------------------------
-- update_api_key_model_cost 触发器函数（用于自动请求的令牌统计）
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION update_api_key_model_cost()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.is_auto_request = TRUE THEN
        UPDATE api_key_model_cost
        SET call_count = call_count + 1,
            total_tokens = total_tokens + COALESCE(NEW.prompt_tokens, 0) + COALESCE(NEW.completion_tokens, 0),
            last_call_at = NEW.ts
        WHERE api_key_id = NEW.api_key_id
          AND model_name = COALESCE(NEW.outbound_model, NEW.client_model);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ----------------------------------------------------------------------------
-- 模型探测回退时间函数
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION model_probe_backoff(consecutive_failures INTEGER)
    RETURNS INTERVAL
    LANGUAGE SQL
    IMMUTABLE
AS $$
    SELECT CASE
        WHEN consecutive_failures <= 0 THEN INTERVAL '30 seconds'
        WHEN consecutive_failures = 1  THEN INTERVAL '2 minutes'
        WHEN consecutive_failures = 2  THEN INTERVAL '5 minutes'
        ELSE                                  INTERVAL '15 minutes'
    END;
$$;

-- ----------------------------------------------------------------------------
-- 获取当前租户的函数
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.get_current_tenant()
RETURNS text
LANGUAGE sql
STABLE
AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;

-- ----------------------------------------------------------------------------
-- 最近成功率计算函数
-- ----------------------------------------------------------------------------
DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int);
DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int, int);
CREATE FUNCTION recent_success_rate(p_credential_id BIGINT,
                                    p_raw_model TEXT,
                                    p_sample_n INT DEFAULT 50,
                                    p_window_hours INT DEFAULT 3)
RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT success
        FROM request_logs
        WHERE credential_id = p_credential_id
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM recent;
$$;

-- ----------------------------------------------------------------------------
-- 请求日志归档函数
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.archive_request_logs(archive_month date)
    RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
AS $func$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint;
    col_list    text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
    END IF;

    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
    INTO col_list
    FROM information_schema.columns a
    JOIN information_schema.columns r
      ON a.table_schema = r.table_schema
     AND a.column_name  = r.column_name
    WHERE a.table_name = 'request_logs_archive'
      AND r.table_name = src_part
      AND a.table_schema = 'public'
      AND a.ordinal_position > 0;

    IF col_list IS NULL OR length(col_list) = 0 THEN
        RAISE EXCEPTION 'No common columns between % and request_logs_archive', src_part;
    END IF;

    EXECUTE format(
        'INSERT INTO %I (%s) SELECT %s FROM %I',
        dst_part, col_list, col_list, src_part
    );
    GET DIAGNOSTICS row_count = ROW_COUNT;

    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, true;
END;
$func$;

-- ----------------------------------------------------------------------------
-- 预创建下月归档分区函数
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.ensure_next_month_archive_partition()
    RETURNS void
    LANGUAGE plpgsql
AS $func$
DECLARE
    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
    partition_name   text := 'request_logs_archive_' || to_char(next_month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, next_month_start, next_month_end
        );
    END IF;
END;
$func$;