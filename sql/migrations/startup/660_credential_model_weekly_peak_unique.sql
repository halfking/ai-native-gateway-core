-- 660: credential_model_weekly_peak 周桶唯一索引
-- 2026-09-05 PG log audit: bg/weekly_peak_rollup.go 的 UPSERT 以
-- ON CONFLICT (week_start, credential_id, raw_model) 为冲突目标，
-- 但建表（sql/schema/01-schema.sql，来自 328a1e5）从未创建匹配的
-- UNIQUE 约束/索引 → 每次周峰值 rollup 报
-- "there is no unique or exclusion constraint matching the ON CONFLICT
-- specification"（SQLSTATE 42P10），周聚合从未成功落行。
--
-- 修复：补唯一索引（ON CONFLICT 推断只要求唯一索引，不要求约束）；
-- 幂等（IF NOT EXISTS），且不重写表数据。已有行经探测无重复
-- (week_start, credential_id, raw_model) 组合；若未来出现重复，
-- CREATE UNIQUE INDEX 会失败上抛而不是静默去重——由部署方人工裁决。
\set ON_ERROR_STOP on

CREATE UNIQUE INDEX IF NOT EXISTS uq_credential_model_weekly_peak_bucket
    ON public.credential_model_weekly_peak (week_start, credential_id, raw_model);

DO $do$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename  = 'credential_model_weekly_peak'
          AND indexname  = 'uq_credential_model_weekly_peak_bucket'
    ) THEN
        RAISE EXCEPTION '660 VALIDATION FAIL: uq_credential_model_weekly_peak_bucket missing';
    END IF;
    RAISE NOTICE '660 VALIDATION OK: uq_credential_model_weekly_peak_bucket present';
END
$do$;
