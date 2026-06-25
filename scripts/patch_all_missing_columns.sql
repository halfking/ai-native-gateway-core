-- ===========================================================================
-- scripts/patch_all_missing_columns.sql
-- 系统补齐所有表缺失的列（确保 Go 端 ensure 函数能成功运行）
-- 幂等：使用 IF NOT EXISTS
-- ===========================================================================

-- work_type_config
ALTER TABLE work_type_config ADD COLUMN IF NOT EXISTS system_prompt TEXT;

-- routing_overrides (期望有 reason, expires_at, enabled)
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS reason TEXT;
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS override_credential_id INT;
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS override_provider_id INT;
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS canonical_name TEXT;
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS raw_model_name TEXT;
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE routing_overrides ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- routing_overrides_audit
ALTER TABLE routing_overrides_audit ADD COLUMN IF NOT EXISTS override_id BIGINT;
ALTER TABLE routing_overrides_audit ADD COLUMN IF NOT EXISTS action TEXT;
ALTER TABLE routing_overrides_audit ADD COLUMN IF NOT EXISTS actor TEXT;
ALTER TABLE routing_overrides_audit ADD COLUMN IF NOT EXISTS payload JSONB;

-- tenant_model_policies
ALTER TABLE tenant_model_policies ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE tenant_model_policies ADD COLUMN IF NOT EXISTS raw_model_name TEXT;
ALTER TABLE tenant_model_policies ADD COLUMN IF NOT EXISTS policy_type TEXT;
ALTER TABLE tenant_model_policies ADD COLUMN IF NOT EXISTS policy_value JSONB;
ALTER TABLE tenant_model_policies ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE tenant_model_policies ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE tenant_model_policies ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- tenant_model_policies_audit
ALTER TABLE tenant_model_policies_audit ADD COLUMN IF NOT EXISTS policy_id BIGINT;
ALTER TABLE tenant_model_policies_audit ADD COLUMN IF NOT EXISTS action TEXT;
ALTER TABLE tenant_model_policies_audit ADD COLUMN IF NOT EXISTS actor TEXT;
ALTER TABLE tenant_model_policies_audit ADD COLUMN IF NOT EXISTS payload JSONB;

-- request_logs: 这些已经齐全
-- ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS work_type TEXT;
-- ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credits_charged BIGINT;

-- model_probe_state: 已有 state 列
-- ALTER TABLE model_probe_state ADD COLUMN IF NOT EXISTS last_unavailable_reason TEXT;
-- ALTER TABLE model_probe_state ADD COLUMN IF NOT EXISTS last_err_code TEXT;
-- ALTER TABLE model_probe_state ADD COLUMN IF NOT EXISTS next_retry_at_override TIMESTAMPTZ;

-- users: 添加列
ALTER TABLE users ADD COLUMN IF NOT EXISTS display_name VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS email VARCHAR(256) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS role VARCHAR(32) NOT NULL DEFAULT 'tenant_admin';
ALTER TABLE users ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE users ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- session_memora_extraction_log
CREATE TABLE IF NOT EXISTS session_memora_extraction_log (
    task_id             TEXT PRIMARY KEY,
    extracted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    written             INT NOT NULL DEFAULT 0,
    skipped_noise       INT NOT NULL DEFAULT 0,
    skipped_duplicate   INT NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'ok',
    detail              JSONB
);

-- session_titles
CREATE TABLE IF NOT EXISTS session_titles (
    task_id             TEXT NOT NULL,
    title               TEXT NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (task_id)
);

-- applications 已经有 display_name
-- providers 添加 quality_fix_mode
ALTER TABLE providers ADD COLUMN IF NOT EXISTS quality_fix_mode TEXT NOT NULL DEFAULT 'off';

-- credentials 添加列
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS concurrency_limit_auto INT;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS fp_slot_limit INT;

-- credential_model_bindings 添加 unavailable_recover_at
ALTER TABLE credential_model_bindings ADD COLUMN IF NOT EXISTS unavailable_at TIMESTAMPTZ;
ALTER TABLE credential_model_bindings ADD COLUMN IF NOT EXISTS unavailable_reason TEXT;
ALTER TABLE credential_model_bindings ADD COLUMN IF NOT EXISTS unavailable_recover_at TIMESTAMPTZ;

-- maas_settings 已存在
-- model_credit_rates 已存在

-- 报告
DO $$ BEGIN
    RAISE NOTICE 'Schema patch completed';
END $$;