-- Down 681: 恢复 665 的 9 段指纹形态（含 LEFT(error_message,200)）。
--
-- 注意：9 段形态与 HEAD Go 聚合器的 8 段 ON CONFLICT 不匹配，
-- 回滚后 bg/provider_error_aggregator.go 每个 tick 会报 42P10。
-- 仅在需要与旧版（7f1604fb9 时代）聚合器代码配对回退时使用。

\set ON_ERROR_STOP on

DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_cred_fingerprint;

CREATE UNIQUE INDEX idx_provider_error_details_tenant_cred_fingerprint
  ON public.provider_error_details (
    COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
    COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
    COALESCE(error_code, ''), COALESCE(LEFT(error_message, 200), ''),
    COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
  );

DELETE FROM public.schema_migrations WHERE version = '681';
