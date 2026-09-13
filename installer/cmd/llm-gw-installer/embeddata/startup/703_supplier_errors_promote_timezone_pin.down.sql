-- Migration 703 down: 恢复 promote_supplier_errors_hot_to_partition 的
-- V371 原始体（无时区钉扎）。
--
-- 与 699 down 同定式：全部使用 CREATE OR REPLACE——回滚运行时 up 侧
-- 定义已存在，裸 CREATE FUNCTION 会在事务内 42710 中断，把 ledger 行
-- 留在原地。703 的 ledger 行在此删除。

BEGIN;

CREATE OR REPLACE FUNCTION public.promote_supplier_errors_hot_to_partition(
    p_retention interval DEFAULT '8 hours',
    p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    moved bigint := 0;
BEGIN
    -- 参数守卫（与 656 模板一致：错误直接上抛，由调用方记录）
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 50000';
    END IF;

    -- 确保目标分区存在（遍历 cold 行的月份）
    PERFORM ensure_supplier_errors_partition(m)
    FROM (
        SELECT DISTINCT date_trunc('month', occurred_at) AS m
        FROM supplier_errors_hot
        WHERE occurred_at < statement_timestamp() - p_retention
        LIMIT 12
    ) months;

    -- 2026-09-05 审计 D-2#2：改为 656 模板式单条 data-modifying CTE。
    -- 旧实现（temp table 复制 → 异常块外 DELETE → INSERT ... EXCEPTION 吞错）
    -- 是 602 迁移注释记录过的真实事故模式：plpgsql EXCEPTION 子块只回滚
    -- 子事务（INSERT），外层 DELETE 照样提交，INSERT 一旦失败该批错误明细
    -- 即静默丢失，且 RAISE WARNING 还宣称 rows preserved in hot table。
    -- 现版本 FOR UPDATE SKIP LOCKED → DELETE...RETURNING（显式列，防
    -- schema drift 按位错配）→ INSERT，单语句原子：任何失败整体回滚、
    -- 错误自然上抛（Go 侧 recordPromoteFailure 记录），不再吞 EXCEPTION。
    WITH batch AS (
        SELECT id FROM supplier_errors_hot
        WHERE occurred_at < statement_timestamp() - p_retention
        ORDER BY occurred_at, id
        LIMIT p_batch_size
        FOR UPDATE SKIP LOCKED
    ), moved_rows AS (
        DELETE FROM supplier_errors_hot h USING batch b
        WHERE h.id = b.id
        RETURNING h.id, h.occurred_at, h.request_id, h.trace_id, h.tenant_id,
                  h.session_id, h.provider_id, h.supplier, h.credential_id,
                  h.model, h.attempt_seq, h.error_type, h.error_code,
                  h.http_status, h.error_message, h.is_retryable, h.stage,
                  h.latency_ms, h.affected_users, h.request_metadata
    ), inserted AS (
        INSERT INTO supplier_errors (
            id, occurred_at, request_id, trace_id, tenant_id,
            session_id, provider_id, supplier, credential_id,
            model, attempt_seq, error_type, error_code,
            http_status, error_message, is_retryable, stage,
            latency_ms, affected_users, request_metadata)
        SELECT id, occurred_at, request_id, trace_id, tenant_id,
               session_id, provider_id, supplier, credential_id,
               model, attempt_seq, error_type, error_code,
               http_status, error_message, is_retryable, stage,
               latency_ms, affected_users, request_metadata
        FROM moved_rows
        RETURNING id
    )
    SELECT count(*) INTO moved FROM inserted;

    RETURN moved;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '703';

COMMIT;
