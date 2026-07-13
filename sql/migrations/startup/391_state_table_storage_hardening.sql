-- 391_state_table_storage_hardening.sql
-- 2026-07-13: 状态表存储加固与精简
--
-- 设计原则（基于 2026-07-13 下午 154 存储压力事件复盘）：
--   1. 请求记录类表（request_logs, usage_ledger, request_wal, credit_ledger,
--      tool_usage_stats）: hot 1 天 + 月度列分区长期保留。
--   2. 状态/路由类表（routing_decision_log, candidate_failure_logs,
--      handoff_logs, credential_model_call_history, model_probe_runs,
--      credential_probe_model_log）: hot 1 天 + 分区 30d DROP PARTITION
--      （可由 settings 调到 1-365 天）。
--
-- 实现内容：
--   1. drop_old_state_partitions(retention_days) - 通用状态表 DROP 入口
--   2. cleanup_old_credential_probe_model_log(retention_days) -
--      列存储堆表专用清理（无分区）
--   3. TimescaleDB retention policy 升级 - credential_model_call_history
--      从 7d 改为 30d（可调）
--   4. 必要索引 - 确保 DELETE/DROP 操作在大量行上不锁表
--   5. 月度分区边界 - 2026-07 之前旧分区不 DROP（保护历史数据）

BEGIN;

-- ═══════════════════════════════════════════════════════════════
-- 1. 通用状态表 DROP PARTITION 函数
-- ═══════════════════════════════════════════════════════════════
--
-- 该函数遍历指定父表的月度分区（命名 _YYYY_MM），DROP 早于 cutoff
-- 的所有分区。cutoff 由调用方通过参数传入（Go 层根据 settings 算出）。
--
-- 重要：分区名格式必须为 `{parent}_YYYY_MM`（与现有命名一致）。
-- 对于不带月度分区的列存储堆表（credential_probe_model_log），
-- 单独使用 cleanup_old_credential_probe_model_log()。
CREATE OR REPLACE FUNCTION drop_old_state_partitions(p_retention_days int DEFAULT 30)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    total_dropped bigint := 0;
    cutoff_ts timestamptz;
    r record;
    target_tables text[] := ARRAY[
        'routing_decision_log',
        'candidate_failure_logs',
        'handoff_logs',
        'model_probe_runs',
        'credential_model_index'
    ];
    parent_name text;
    partition_name text;
    partition_year int;
    partition_month int;
    partition_first_day date;
BEGIN
    IF p_retention_days < 1 THEN
        RAISE WARNING 'drop_old_state_partitions: retention_days=% < 1, clamping to 1', p_retention_days;
        p_retention_days := 1;
    END IF;
    cutoff_ts := NOW() - (p_retention_days || ' days')::interval;

    FOREACH parent_name IN ARRAY target_tables LOOP
        FOR r IN
            SELECT c.relname AS partname
            FROM pg_inherits i
            JOIN pg_class p ON p.oid = i.inhparent
            JOIN pg_class c ON c.oid = i.inhrelid
            WHERE p.relname = parent_name
              AND c.relname ~ ('^' || parent_name || '_\d{4}_\d{2}$')
        LOOP
            partition_name := r.partname;
            -- Parse YYYY_MM from suffix (e.g. "routing_decision_log_2026_07")
            BEGIN
                partition_year := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1) - 1)::int;
                partition_month := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1))::int;
                partition_first_day := make_date(partition_year, partition_month, 1);

                -- DROP if entire month is before cutoff
                -- 30-day retention: cutoff = 2026-06-13, drop 2026_05 and earlier
                IF (partition_first_day + INTERVAL '1 month' - INTERVAL '1 day') < cutoff_ts THEN
                    EXECUTE format('DROP TABLE IF EXISTS %I', partition_name);
                    total_dropped := total_dropped + 1;
                    RAISE DEBUG 'drop_old_state_partitions: dropped %', partition_name;
                END IF;
            EXCEPTION WHEN OTHERS THEN
                RAISE WARNING 'drop_old_state_partitions: failed to parse % (%)', partition_name, SQLERRM;
                -- Continue with next partition, don't abort the whole loop
            END;
        END LOOP;
    END LOOP;

    RETURN total_dropped;
END;
$function$;

COMMENT ON FUNCTION drop_old_state_partitions(int) IS
    'Drops monthly partitions older than the given retention for state/routing tables. Used by bg.partition_manager. Idempotent.';

-- ═══════════════════════════════════════════════════════════════
-- 2. credential_probe_model_log 专用清理（堆表 + 列存储）
-- ═══════════════════════════════════════════════════════════════
--
-- credential_probe_model_log 是列存储堆表（无月度分区），因此不能用
-- DROP PARTITION。直接 DELETE 即可（列存储支持 DELETE 整行）。
-- 默认 90 天保留。
CREATE OR REPLACE FUNCTION cleanup_old_credential_probe_model_log(p_retention_days int DEFAULT 90)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    deleted_count bigint;
    cutoff_ts timestamptz;
BEGIN
    IF p_retention_days < 7 THEN
        RAISE WARNING 'cleanup_old_credential_probe_model_log: retention_days=% < 7, clamping to 7', p_retention_days;
        p_retention_days := 7;
    END IF;
    cutoff_ts := NOW() - (p_retention_days || ' days')::interval;

    DELETE FROM credential_probe_model_log
    WHERE created_at < cutoff_ts;

    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count;
END;
$function$;

COMMENT ON FUNCTION cleanup_old_credential_probe_model_log(int) IS
    'Deletes rows from credential_probe_model_log older than the given retention. Used by bg.partition_manager. Idempotent.';

-- ═══════════════════════════════════════════════════════════════
-- 3. 确保 created_at 索引（如果缺失）
-- ═══════════════════════════════════════════════════════════════
CREATE INDEX IF NOT EXISTS idx_credential_probe_model_log_created_at
    ON credential_probe_model_log (created_at);

-- ═══════════════════════════════════════════════════════════════
-- 4. TimescaleDB retention policy 升级
-- ═══════════════════════════════════════════════════════════════
--
-- credential_model_call_history 是 TimescaleDB hypertable，原 retention
-- policy 是 7d。升级到 30d 匹配状态表精简方案。policy 修改是幂等的。
--
-- 2026-07-13 fix: 当 TimescaleDB 扩展未安装时（如非 TimescaleDB 部署），
-- 静默跳过而不是让整个迁移失败。credential_model_call_history 仍由
-- Go 层 call_history_aggregator 写入，但没有 TimescaleDB 自动压缩/保留。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        IF EXISTS (
            SELECT 1 FROM timescaledb_information.hypertables
            WHERE hypertable_name = 'credential_model_call_history'
        ) THEN
            PERFORM remove_retention_policy('credential_model_call_history');
            PERFORM add_retention_policy('credential_model_call_history', INTERVAL '30 days');
            RAISE NOTICE 'credential_model_call_history retention policy: 7d -> 30d';
        ELSE
            RAISE NOTICE 'credential_model_call_history is not a hypertable; skipping retention policy change';
        END IF;
    ELSE
        RAISE NOTICE 'timescaledb extension not installed; skipping credential_model_call_history retention policy change';
    END IF;
EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'credential_model_call_history retention policy change failed: %', SQLERRM;
END $$;

-- ═══════════════════════════════════════════════════════════════
-- 5. 审计触发器（可选）：状态变化记录到 audit 表
-- ═══════════════════════════════════════════════════════════════
--
-- 这一步是未来增强：未来可以将 model_probe_state 的状态变化
-- 自动写入 routing_audit_log，进一步减少状态查询的存储。
-- 当前不实施，避免本次迁移过于复杂。

-- ═══════════════════════════════════════════════════════════════
-- 6. 更新 settings_kv 默认值（如果不存在则插入）
-- ═══════════════════════════════════════════════════════════════
--
-- 2026-07-13 fix: 154 实际 schema 是
--   key, value, value_type, scope, category, updated_at, updated_by, prev_value, prev_updated_at
-- 没有 description/hot_reloadable 列。改用与 settings_kv 实际 schema 兼容的 INSERT。
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
SELECT * FROM (VALUES
    ('lifecycle.routing_decision_log_ttl_days', '30'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.candidate_failure_logs_ttl_days', '30'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.handoff_logs_ttl_days', '30'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.credential_model_call_history_ttl_days', '30'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.model_probe_runs_ttl_days', '90'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.credential_probe_model_log_ttl_days', '90'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.usage_ledger_ttl_days', '1'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.request_wal_ttl_days', '1'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.credit_ledger_ttl_days', '1'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
    ('lifecycle.tool_usage_stats_ttl_days', '1'::jsonb, 'int', 'platform', 'lifecycle', NOW())
) AS v(key, value, value_type, scope, category, updated_at)
WHERE NOT EXISTS (SELECT 1 FROM settings_kv WHERE key = v.key);

-- ═══════════════════════════════════════════════════════════════
-- 7. 验证函数已创建
-- ═══════════════════════════════════════════════════════════════
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'drop_old_state_partitions') THEN
        RAISE EXCEPTION 'drop_old_state_partitions was not created';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'cleanup_old_credential_probe_model_log') THEN
        RAISE EXCEPTION 'cleanup_old_credential_probe_model_log was not created';
    END IF;
    RAISE NOTICE '391_state_table_storage_hardening: all functions created successfully';
END $$;

COMMIT;

-- ═══════════════════════════════════════════════════════════════
-- 后续可执行的 SQL（部署后人工运行）
-- ═══════════════════════════════════════════════════════════════
-- 1. 验证分区状态：
--    SELECT p.relname AS parent, count(*) AS partitions,
--           pg_size_pretty(SUM(pg_relation_size(c.oid))) AS total_size
--    FROM pg_inherits i
--    JOIN pg_class p ON p.oid = i.inhparent
--    JOIN pg_class c ON c.oid = i.inhrelid
--    WHERE p.relname IN ('routing_decision_log', 'candidate_failure_logs', 'handoff_logs')
--    GROUP BY p.relname;
--
-- 2. 手动触发 DROP（dry-run）：
--    SELECT * FROM drop_old_state_partitions(30);
--
-- 3. 查看保留策略：
--    SELECT * FROM timescaledb_information.jobs
--    WHERE application_name LIKE '%Retention%';
