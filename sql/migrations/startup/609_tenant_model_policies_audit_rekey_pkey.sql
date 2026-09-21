-- ===========================================================================
-- File:          sql/migrations/startup/609_tenant_model_policies_audit_rekey_pkey.sql
-- Migration:     609
-- Database:      llm_gateway
-- Purpose:       修复 tenant_model_policies_audit 的重复 id 并补主键
--
-- Status:        active
-- Idempotent:    YES (重复 id 重发逻辑天然收敛 + pg_constraint 判存 + 单事务)
-- Dependencies:  024_tenant_model_policies（audit 表必须已存在）；建议与 608 同批发布
--
-- Background:
--   1. 与 608 同源漂移：migration 024 声明 audit 表 `id BIGSERIAL PRIMARY KEY`，
--      生产 252 实际无主键。2026-08-27 实测 252：20 行仅 4 个 id（1-4，4 组
--      重复），序列 tenant_model_policies_audit_id_seq last_value=4 —— 历史
--      数据写入路径未按序列取值，产生重复 id（seed 文件不含此表，排除
--      seed 来源）。
--   2. 无主键 + 重复 id 使审计行无法按 id 定位，与 024 声明形状持续漂移。
--
-- Data change (⚠️ 本迁移包含数据修复，需单独评审):
--   - 重复 id 组内保留一行（按 ts 最早、ctid 决胜），其余行改派
--     nextval(sequence) 新 id。审计行一行不删；仅"重复副本"的合成 id 变化。
--   - audit.id 是合成行号，无外键引用（policy_id 列指向
--     tenant_model_policies.id，不受影响）；re-key 不可逆（原重复 id 不保留）。
--   - NULL id（若存在）同样改派序列新值。
--   - 数据守卫在 re-key 之后执行：任何残留 NULL/重复都会中止迁移（回滚整个
--     事务），不存在"静默修数据"路径。
--
-- RLS note: 执行角色需能看到全表行。252 上迁移执行用户 llm_gateway 为
--   superuser/bypassrls（2026-08-27 验证）。FORCE RLS 表由 superuser 全量可见。
--
-- Estimated Time:   <1s（20 行表）
-- Affected Rows:    252 上 16 行（重复副本 re-key）；干净环境 0 行
-- Breaking Change:  NO（审计 trigger INSERT 不写 id 列，默认取序列，PK 不影响）
-- Rollback Script:  609_tenant_model_policies_audit_rekey_pkey.down.sql
--                   （仅回滚约束；re-key 的数据变更不可逆）
--
-- Date: 2026-08-27
-- Author: ZCode (dbx Phase 4 —— PK 漂移修复裁决)
-- Refs: docs/03-design/04-data-design/数据库处理框架方案.md §7.10
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

-- ─── 1. NULL id 改派序列新值（若存在）───
UPDATE public.tenant_model_policies_audit
   SET id = nextval('public.tenant_model_policies_audit_id_seq')
 WHERE id IS NULL;

-- ─── 2. 重复 id 组内 re-key：保留每组最早一行（ts，ctid 决胜），副本改派新 id ───
-- ctid 在单条 UPDATE 的快照内稳定，是重复组内无业务主键时唯一可靠的行定位。
WITH ranked AS (
    SELECT ctid,
           row_number() OVER (PARTITION BY id ORDER BY ts ASC, ctid) AS rn
      FROM public.tenant_model_policies_audit
)
UPDATE public.tenant_model_policies_audit a
   SET id = nextval('public.tenant_model_policies_audit_id_seq')
  FROM ranked r
 WHERE a.ctid = r.ctid
   AND r.rn > 1;

DO $$
DECLARE
    v_null BIGINT;
    v_dup  BIGINT;
    v_pkey_count BIGINT;
    v_max_id BIGINT;
    v_seq_last BIGINT;
BEGIN
    -- ─── 3. 数据守卫：re-key 后必须干净，否则中止（整事务回滚）───
    SELECT count(*) FILTER (WHERE id IS NULL),
           count(*) - count(DISTINCT id)
      INTO v_null, v_dup
      FROM public.tenant_model_policies_audit;

    IF v_null > 0 OR v_dup > 0 THEN
        RAISE EXCEPTION
            'migration 609 aborted: public.tenant_model_policies_audit.id still has % NULL / % duplicate values after re-key; investigate before retrying',
            v_null, v_dup;
    END IF;

    -- ─── 4. 序列兜底：若 max(id) 领先序列则对齐（正常路径 re-key 已推进序列）───
    SELECT COALESCE(max(id), 0) INTO v_max_id
      FROM public.tenant_model_policies_audit;
    SELECT last_value INTO v_seq_last
      FROM public.tenant_model_policies_audit_id_seq;
    IF v_max_id > v_seq_last THEN
        PERFORM setval('public.tenant_model_policies_audit_id_seq', v_max_id, true);
    END IF;

    -- ─── 5. 幂等补主键（pg_constraint 判存）───
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tenant_model_policies_audit_pkey'
          AND conrelid = 'public.tenant_model_policies_audit'::regclass
    ) THEN
        ALTER TABLE public.tenant_model_policies_audit
            ADD CONSTRAINT tenant_model_policies_audit_pkey PRIMARY KEY (id);
    END IF;

    -- ─── 6. 事后校验：必须恰好存在一个主键 ───
    SELECT count(*)
      INTO v_pkey_count
      FROM pg_index i
      JOIN pg_class c ON c.oid = i.indrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname = 'tenant_model_policies_audit'
       AND i.indisprimary;

    IF v_pkey_count <> 1 THEN
        RAISE EXCEPTION
            'migration 609 post-check failed: expected exactly 1 primary key on public.tenant_model_policies_audit, found %',
            v_pkey_count;
    END IF;

    RAISE NOTICE 'migration 609: audit re-key + tenant_model_policies_audit_pkey ensured (id clean: % null / % dup)', v_null, v_dup;
END $$;

COMMIT;

-- ===========================================================================
-- Post-deploy verification (rule 38 §6.5):
--   ssh -p 25022 root@115.29.212.252 docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway
--   SELECT count(*) AS dup_ids FROM (
--     SELECT id FROM public.tenant_model_policies_audit GROUP BY id HAVING count(*) > 1) d;
--   expected: 0
--   SELECT i.indisprimary, ic.relname
--     FROM pg_index i
--     JOIN pg_class c ON c.oid = i.indrelid
--     JOIN pg_class ic ON ic.oid = i.indexrelid
--     JOIN pg_namespace n ON n.oid = c.relnamespace
--    WHERE n.nspname = 'public' AND c.relname = 'tenant_model_policies_audit'
--      AND i.indisprimary;
--   expected: 1 row (tenant_model_policies_audit_pkey)
-- ===========================================================================
