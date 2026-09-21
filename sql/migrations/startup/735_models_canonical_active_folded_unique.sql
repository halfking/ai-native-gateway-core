-- 735 (R51 审计轮, 2026-09-21): uq_models_canonical_active_folded_name
--
-- R50 F19 归一化对账专项的收口（R50 handoff §R51.1 明文规格）：models_canonical
-- active 行的表达式唯一索引，索引表达式与 modelname.DedupCanonicalNameSQL 的
-- run-collapse 臂逐字一致（折叠链 lower + ['.','-',' ','/'] → '_'，再
-- regexp_replace '[-_]{2,}' → '_'）。该索引是 admin createModel 归一化查重
-- （DedupCanonicalNameSQL）在并发窗口下的 DB 兜底，调用方捕获 23505 转语义
-- 409。
--
-- 前置（fail-closed）：若库中仍存在 active 折叠重复对，本迁移报错拒绝执行，
-- 并指路对账脚本。理由：models_canonical 承载 342 迁移的 ON DELETE CASCADE
-- 外键族（credential_model_index_hot / tenant_model_policies /
-- model_capability_profiles / model_substitution_overrides /
-- role_task_llm_mapping 等 10 表），机械删 loser 会无声级联删引用数据；
-- 折叠重复对的逐对裁决 + 引用重映射是运维决策，唯一通道是
-- sql/fixes/2026-09-20-canonical-dedup-cleanup.sql（备份+重映射+V 校验，
-- 幂等可重入）。新装环境 / 已对账环境（252 共享库 2026-09-21 已清零）
-- 本守卫自然通过。
--
-- 幂等：守卫可重复执行；索引 IF NOT EXISTS，重跑 no-op。
-- 表达式字节级契约由 migration_735_test.go 钉死（C1 解析
-- modelname/canonical_dedup.go 的 foldedNameExpr 合成期望值，禁止手改本文件
-- 折叠链）。

DO $$
DECLARE
  dup_groups int;
BEGIN
  SELECT count(*) INTO dup_groups FROM (
    SELECT 1 FROM models_canonical
    WHERE status = 'active'
    GROUP BY regexp_replace(replace(replace(replace(replace(lower(canonical_name), '.', '_'), '-', '_'), ' ', '_'), '/', '_'), '[-_]{2,}', '_', 'g')
    HAVING count(*) > 1
  ) g;
  IF dup_groups > 0 THEN
    RAISE EXCEPTION
      '735 precheck: % active fold-duplicate group(s) in models_canonical — run sql/fixes/2026-09-20-canonical-dedup-cleanup.sql (backup + reference remap, idempotent) first, then re-run this migration',
      dup_groups;
  END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_models_canonical_active_folded_name
  ON models_canonical (
    regexp_replace(replace(replace(replace(replace(lower(canonical_name), '.', '_'), '-', '_'), ' ', '_'), '/', '_'), '[-_]{2,}', '_', 'g')
  )
  WHERE status = 'active';
