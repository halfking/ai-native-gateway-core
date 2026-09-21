-- ===========================================================================
-- File:          sql/migrations/startup/608_tenant_model_policies_add_pkey.sql
-- Migration:     608
-- Database:      llm_gateway
-- Purpose:       补齐 tenant_model_policies 在生产环境缺失的主键约束
--
-- Status:        active
-- Idempotent:    YES (pg_constraint 判存 + 单事务包裹)
-- Dependencies:  024_tenant_model_policies（表必须已存在）
--
-- Background:
--   1. Migration 024 与 db/db.go 幂等建表都声明 `id BIGSERIAL PRIMARY KEY`，
--      但生产 252（172.16.2.210）的 tenant_model_policies 没有主键约束——
--      2026-08-27 通过 pg_index 实时确认：该表仅有的索引是
--      idx_tmp_canonical / idx_tmp_tenant_active（partial）/
--      tenant_model_policies_tenant_id_canonical_name_key（UNIQUE），
--      id 列上没有任何单列唯一性保证。
--   2. 根因：从 baseline dump（sql/schema/01-schema.sql）初始化的环境继承了
--      "无 PK" 形状；migration 024 的 CREATE TABLE IF NOT EXISTS 对已存在的
--      表短路，主键永远不会被补上。sql/schema/01-schema.sql 与
--      sql/objects/constraints/ 已同步补上 pkey 条目，本迁移修复存量环境。
--   3. 影响面不止 dbx 门禁（internal/dbx 报 DriftPKMismatch，blocking）：
--      admin/model_policies.go 的 UPDATE ... WHERE id = $1 在无单列唯一键时
--      没有行数保证（单列唯一键 blocking 规则见
--      docs/03-design/04-data-design/数据库处理框架方案.md §7.10）。
--
-- Data safety (2026-08-27 实测 252):
--   - 3 行，id 0 NULL / 0 重复，序列 tenant_model_policies_id_seq last_value=10
--     与 max(id)=10 一致 → 可直接补 PK。
--   - 本迁移绝不改数据：DO 块先验证 id 无 NULL/重复，否则 RAISE 中止，
--     数据修复必须走另行评审的迁移（见 608 对 audit 表的 re-key 先例）。
--
-- Estimated Time:   <1s（3 行表，ADD CONSTRAINT 仅建 btree 索引）
-- Affected Rows:    0（纯 DDL）
-- Breaking Change:  NO（新增约束只会拒绝未来写入重复/NULL id，存量已验证干净）
-- Rollback Script:  608_tenant_model_policies_add_pkey.down.sql
--
-- Date: 2026-08-27
-- Author: ZCode (dbx Phase 4 —— PK 漂移修复裁决)
-- Refs: docs/03-design/04-data-design/数据库处理框架方案.md §7.10
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

DO $$
DECLARE
    v_null BIGINT;
    v_dup  BIGINT;
    v_pkey_count BIGINT;
BEGIN
    -- ─── 1. 数据守卫：PK 修复绝不顺带改数据 ───
    SELECT count(*) FILTER (WHERE id IS NULL),
           count(*) - count(DISTINCT id)
      INTO v_null, v_dup
      FROM public.tenant_model_policies;

    IF v_null > 0 OR v_dup > 0 THEN
        RAISE EXCEPTION
            'migration 608 aborted: public.tenant_model_policies.id has % NULL / % duplicate values; repair the data via a separately reviewed migration first (this migration never mutates rows)',
            v_null, v_dup;
    END IF;

    -- ─── 2. 幂等补主键（pg_constraint 判存）───
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tenant_model_policies_pkey'
          AND conrelid = 'public.tenant_model_policies'::regclass
    ) THEN
        ALTER TABLE public.tenant_model_policies
            ADD CONSTRAINT tenant_model_policies_pkey PRIMARY KEY (id);
    END IF;

    -- ─── 3. 事后校验：必须恰好存在一个主键 ───
    SELECT count(*)
      INTO v_pkey_count
      FROM pg_index i
      JOIN pg_class c ON c.oid = i.indrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname = 'tenant_model_policies'
       AND i.indisprimary;

    IF v_pkey_count <> 1 THEN
        RAISE EXCEPTION
            'migration 608 post-check failed: expected exactly 1 primary key on public.tenant_model_policies, found %',
            v_pkey_count;
    END IF;

    RAISE NOTICE 'migration 608: tenant_model_policies_pkey ensured (id clean: % null / % dup)', v_null, v_dup;
END $$;

COMMIT;

-- ===========================================================================
-- Post-deploy verification (rule 38 §6.5):
--   ssh -p 25022 root@115.29.212.252 docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway
--   SELECT i.indisprimary, ic.relname
--     FROM pg_index i
--     JOIN pg_class c ON c.oid = i.indrelid
--     JOIN pg_class ic ON ic.oid = i.indexrelid
--     JOIN pg_namespace n ON n.oid = c.relnamespace
--    WHERE n.nspname = 'public' AND c.relname = 'tenant_model_policies'
--      AND i.indisprimary;
--   expected: 1 row (tenant_model_policies_pkey)
-- ===========================================================================
