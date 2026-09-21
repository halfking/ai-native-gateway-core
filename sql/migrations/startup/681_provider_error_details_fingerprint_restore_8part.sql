-- Migration 681: provider_error_details 指纹索引收敛回 8 段（664/E-#3 权威形态）
--
-- 编号说明：原编号 678，与远端 678_request_logs_bodies_hot_unique_repair 撞号，
-- 2026-09-07 rebase 时重编号至 681。
--
-- 背景（2026-09-07 24h 审计 P0 裁决）：
--   664（E-#3）把聚合指纹定为 8 段——error_message 移出键、降级为样本列，
--   bg/provider_error_aggregator.go 的 ON CONFLICT 目标同步改为 8 段。
--   665 由并行会话基于 664 之前的旧世界（639/V368 的 9 段契约）编写，
--   会把索引重建回含 LEFT(error_message,200) 的 9 段。
--   部署轨道按 664→665 执行后，库内为 9 段索引而 HEAD Go 代码为 8 段
--   ON CONFLICT，每个聚合 tick 必报 SQLSTATE 42P10，
--   provider_error_details（凭据维度错误集合）从此停写。
--
-- 裁决：664 + Go 8 段为权威。665 已从 apply-db-revision-sequence.sh
--   移除（文件保留在仓库，已执行过 665 的库的 checksum 台账不受影响），
--   本迁移把所有库态收敛到 8 段：
--     - 已跑过坏 665（9 段索引）→ 检测 error_message 段存在，重建为 8 段；
--     - 只跑过 664 / 全新安装（8 段索引）→ no-op；
--     - find|sort 全量重放（664→665→…→681）→ 最终 8 段，正确收敛。
--
-- 幂等：索引缺失或已为 8 段时不做任何 DDL。
--
-- 2026-09-07

\set ON_ERROR_STOP on

DO $$
DECLARE
  idx_def text;
BEGIN
  SELECT pg_get_indexdef(c.oid)
    INTO idx_def
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relname = 'idx_provider_error_details_tenant_cred_fingerprint'
     AND n.nspname = 'public'
     AND c.relkind = 'i'
   LIMIT 1;

  IF idx_def IS NOT NULL AND position('error_message' in idx_def) > 0 THEN
    DROP INDEX public.idx_provider_error_details_tenant_cred_fingerprint;
    CREATE UNIQUE INDEX idx_provider_error_details_tenant_cred_fingerprint
      ON public.provider_error_details (
        COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
        COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
        COALESCE(error_code, ''), COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
      );
    RAISE NOTICE '681: rebuilt fingerprint index to 8-part (error_message demoted to sample column, E-#3)';
  ELSE
    RAISE NOTICE '681: fingerprint index already 8-part or absent, nothing to do';
  END IF;
END
$$;

COMMENT ON INDEX idx_provider_error_details_tenant_cred_fingerprint IS
'provider_error_details 聚合指纹（681 收敛回 664/E-#3 的 8 段权威形态；error_message 为样本列）';

-- 台账修复：坏 665 曾把版本错记为 '663'（与 663_training_export 撞车且被
-- ON CONFLICT 吞掉或顶替语义）。这里按真实编号补记 665（已废弃）与 681，
-- 使 checksum 台账能看到两者的真实存在。
INSERT INTO public.schema_migrations (version, description)
VALUES ('665', 'SUPERSEDED (wrong 9-part shape, reverted by 681; was mis-recorded as 663)')
ON CONFLICT (version) DO NOTHING;

INSERT INTO public.schema_migrations (version, description)
VALUES ('681', 'provider_error_details fingerprint restore to 8-part (664/E-#3 authoritative)')
ON CONFLICT (version) DO NOTHING;
