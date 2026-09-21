-- 693_provider_models_canonical_cleared_at.sql
-- POST_CONDITION: SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='provider_models' AND column_name='canonical_cleared_at' HAVING count(*) = 1;
--
-- 管理员解绑持久化标记：provider_models.canonical_cleared_at。
--
-- 背景：管理端 PATCH /api/providers/{id}/models/{oid} 的 clear_canonical 分支把
-- provider_models.canonical_id 置 NULL 后，下一轮 discovery 刷新会通过
-- modelcatalog.UpsertCredentialModel 的 ON CONFLICT 分支
-- （canonical_id = COALESCE(EXCLUDED.canonical_id, provider_models.canonical_id)，
-- discovery 恒传非 NULL）把关联静默写回 —— 运营者的解绑最多存活一个 discovery 周期。
--
-- 本迁移只加一列可空时间戳：
--   NULL                —— 未解绑（默认），discovery 可照常维护 canonical_id；
--   非空（解绑时间）    —— 运营者已显式解绑，自动路径（discovery upsert /
--                          provider 手动刷新）不得再写 canonical_id。
--
-- 写入/清除语义（Go 侧配合，见 modelcatalog/upsert.go 与
-- admin/provider_offer_force_recover.go）：
--   - clear_canonical          → canonical_id = NULL, canonical_cleared_at = now()
--   - 显式重新关联（PATCH canonical_id / 手工加入选定标准模型）→ canonical_cleared_at = NULL
--   - discovery upsert          → canonical_cleared_at 非空的行保持原 canonical_id 不变
--
-- 单列、可空、无视图/触发器依赖；Idempotent: IF NOT EXISTS 守卫，可重复执行。

BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'provider_models'
      AND column_name = 'canonical_cleared_at'
  ) THEN
    ALTER TABLE public.provider_models ADD COLUMN canonical_cleared_at timestamp with time zone;
    COMMENT ON COLUMN public.provider_models.canonical_cleared_at IS '管理员解绑标记。非空表示运营者已显式解绑 canonical_id，discovery 等自动路径不得写回 canonical_id；显式重新关联时置回 NULL。';
  END IF;
END $$;

COMMIT;
