-- Migration 703: pin promote_supplier_errors_hot_to_partition month
-- grouping to Asia/Shanghai (F13, audit 2026-09-13-r20 §二.4).
--
-- 699 pinned ensure_supplier_errors_partition (the V371-track function
-- 694's sweep missed); 698 pinned the nine objects/-canonical promote
-- bodies but supplier_errors promote lives only on the V371 deploy track
-- with no objects/functions canonical — the exact asymmetry the audit's
-- promote-family contract blind spot calls out. Unpinned, an
-- Asia/Shanghai month start falling in a UTC session routes the whole
-- batch into the PREVIOUS month group, ensure_* then creates a partition
-- whose 694/699-pinned Shanghai bounds do not contain the rows, and the
-- INSERT dies with 23514 — stranding the batch in supplier_errors_hot
-- (the same latent defect migration 698 closed for the nine).
--
-- Fix: `SET LOCAL TIME ZONE 'Asia/Shanghai'` as the first statement of
-- the function body (694/698/699 pattern), so month grouping always
-- matches the ensure_* bounds convention regardless of the caller
-- session. The body is the V371 definition (656-template atomic
-- single-statement CTE with FOR UPDATE SKIP LOCKED -> DELETE RETURNING
-- -> INSERT, per audit D-2#2) plus the pin; nothing else changes.
--
-- Ledger: INSERT ... ON CONFLICT DO UPDATE (695/696/697/698/699
-- precedent). The three 01-schema.sql baselines gain the pinned body so
-- fresh installs match running clusters; the promote three-way contract
-- test gains the supplier_errors entry (audit §二.4 promote-family
-- blind-spot closure). The supplier_errors table family itself is NOT in
-- the baselines (V371 objects never were) — the pinned promote body sits
-- after the 699-pinned ensure body, matching the live-cluster layout
-- where the ensure function is defined by baseline and the promote
-- function by V371's migration body.
--
-- Down migration restores the V371 original body (CREATE OR REPLACE —
-- bare CREATE would 42710 inside the transaction; 699 down precedent).

BEGIN;

--
-- Name: promote_supplier_errors_hot_to_partition(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.promote_supplier_errors_hot_to_partition(
    p_retention interval DEFAULT '8 hours',
    p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    moved bigint := 0;
BEGIN
    -- 698/703 时区钉扎：月份分组必须与会话时区无关，否则 UTC 会话下
    -- 月初 [00:00,08:00) +08 的行落入上月分区组，ensure_* 按 694/699
    -- 钉扎的上海边界建出的分区不含这些行，INSERT 以 23514 中断，整批
    -- 滞留 supplier_errors_hot。SET LOCAL 紧跟 BEGIN、先于一切
    -- date_trunc / statement_timestamp 求值（699 定式）。
    SET LOCAL TIME ZONE 'Asia/Shanghai';

    -- 参数守卫（与 656 模板一致：错误直接上抛，由调用方记录）
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 50000';
    END IF;

    -- 确保目标分区存在（遍历 cold 行的月份）。months CTE 的
    -- date_trunc('month', ...) 求值发生在钉扎之后：plpgsql 逐语句
    -- 执行，内联进 PERFORM 的子查询在 SET LOCAL 已生效的事务内运行。
    PERFORM ensure_supplier_errors_partition(m)
    FROM (
        SELECT DISTINCT date_trunc('month', occurred_at) AS m
        FROM supplier_errors_hot
        WHERE occurred_at < statement_timestamp() - p_retention
        ORDER BY 1
        LIMIT 12
    ) months;

    -- 2026-09-05 审计 D-2#2：656 模板式单条 data-modifying CTE。
    -- 旧实现（temp table 复制 → 异常块外 DELETE → INSERT ... EXCEPTION
    -- 吞错）是 602 迁移注释记录过的真实事故模式：plpgsql EXCEPTION
    -- 子块只回滚子事务（INSERT），外层 DELETE 照样提交，INSERT 一旦
    -- 失败该批错误明细即静默丢失，且 RAISE WARNING 还宣称 rows
    -- preserved in hot table。现版本 FOR UPDATE SKIP LOCKED →
    -- DELETE...RETURNING（显式列，防 schema drift 按位错配）→
    -- INSERT，单语句原子：任何失败整体回滚、错误自然上抛（Go 侧
    -- recordPromoteFailure 记录），不再吞 EXCEPTION。
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

COMMENT ON FUNCTION promote_supplier_errors_hot_to_partition(interval, int) IS
'Promotes cold rows from supplier_errors_hot to monthly columnar partitions
(default retention 8h, batch 5000). Atomic single data-modifying CTE
(FOR UPDATE SKIP LOCKED -> DELETE RETURNING -> INSERT; 2026-09-05 audit
D-2#2 replaced the temp-table + EXCEPTION body that could silently drop a
batch on INSERT failure). Month grouping pinned to Asia/Shanghai (703);
matches the 699-pinned ensure_supplier_errors_partition bounds.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('703', 'Pin promote_supplier_errors month grouping to Asia/Shanghai (V371 track, F13)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
