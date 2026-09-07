-- Migration 684: 清除残留的 idx_provider_error_details_tenant_fingerprint(665 时代形状)
--
-- Background (2026-09-07 部署后观察, 聚合器 tick 23505 间歇复现):
--   bg/provider_error_aggregator.go 的 INSERT 以
--   idx_provider_error_details_tenant_cred_fingerprint(8 段含 credential_id、
--   error_message 为样本列)为 ON CONFLICT 仲裁者 —— 664/E-#3 的权威身份模型,
--   681 负责把它收敛回 8 段。
--
--   但本库还残留另一个 idx_provider_error_details_tenant_fingerprint
--   (tenant, provider, model, endpoint, type, code, LEFT(error_message,200),
--   bucket —— 坏 665 的形状)。664 的设计本应 DROP 它(664:109-111),但 665
--   又把它建了回来,681 只收敛 cred 那个,于是两个唯一索引语义打架:
--   同一 (tenant,provider,model,endpoint,type,code,bucket) 桶、消息样例
--   左 200 相同、credential_id 不同 —— cred 仲裁者判"无冲突",INSERT 落库
--   时撞 tenant_fingerprint → SQLSTATE 23505,聚合 tick 整体失败。
--
-- Fix: 删除残留索引,恢复 664 的单指纹形态。守卫:仅当权威的
--   tenant_cred_fingerprint 存在且不含 error_message 段时才 DROP,
--   避免异常库失去全部指纹索引。
--
-- Idempotent: YES(DROP INDEX IF EXISTS)。

DO $$
DECLARE
  v_cred_idx_def text;
BEGIN
  SELECT indexdef INTO v_cred_idx_def
    FROM pg_indexes
   WHERE schemaname = 'public'
     AND indexname  = 'idx_provider_error_details_tenant_cred_fingerprint';

  IF v_cred_idx_def IS NULL THEN
    RAISE NOTICE '684: authoritative idx_provider_error_details_tenant_cred_fingerprint missing; keeping tenant_fingerprint as-is';
    RETURN;
  END IF;

  IF position('error_message' in v_cred_idx_def) > 0 THEN
    RAISE NOTICE '684: authoritative fingerprint is message-carrying (pre-664 shape); keeping tenant_fingerprint as-is';
    RETURN;
  END IF;

  DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_fingerprint;
  RAISE NOTICE '684: dropped stale idx_provider_error_details_tenant_fingerprint (665 leftover; 664/E-#3 single-fingerprint restored)';
END
$$;
