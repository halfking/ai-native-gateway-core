-- 076-credential-keys.sql
-- Per-credential multi-key rotation support (OmniRoute apiKeyRotator 对齐).
--
-- 背景: 旧实现 1 credential = 1 encrypted key (credentials.secret_ciphertext).
-- 同一家免费 provider 注册 N 个账号只能建 N 个 credential, 管理负担重且
-- 无法共享 concurrency/breaker 配置. 本迁移新增 credential_keys 子表, 让
-- 一个 credential 挂多个 API key, 配合 domains/credential/keyrotator.go
-- 做 round-robin 轮转 + per-key 健康追踪, N 倍放大免费额度.
--
-- 设计要点:
--   - 主 key 仍留在 credentials.secret_ciphertext (向后兼容, 旧读取路径不变).
--   - credential_keys 存 EXTRA keys (kid_index 从 1 起; 0 保留给主 key 的逻辑位).
--   - per-key 健康 (status / consecutive_failures) 在内存 KeyRotator 维护,
--     不落 DB (高频写); 此表的 status 列仅用于跨进程共享"已标记 invalid 的
--     terminal key" (如 402 余额耗尽), 由 admin 手动充值后重置.
--   - 与入站 api_keys 表完全独立 (那是调用方 key, 这是上游 credential key).

CREATE TABLE IF NOT EXISTS public.credential_keys (
    id                    BIGSERIAL    PRIMARY KEY,
    credential_id         BIGINT       NOT NULL REFERENCES public.credentials(id) ON DELETE CASCADE,
    kid_index             INT          NOT NULL,   -- 逻辑位 (1, 2, 3...; 0=主 key 不入此表)
    secret_ciphertext     BYTEA        NOT NULL,   -- 复用 secret.EncryptAESGCM envelope
    status                TEXT         NOT NULL DEFAULT 'active',  -- active | invalid
    label                 TEXT,
    last_used_at          TIMESTAMPTZ,
    last_failed_at        TIMESTAMPTZ,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    tenant_id             TEXT         NOT NULL DEFAULT 'default',
    CONSTRAINT credential_keys_cred_kid_key UNIQUE (credential_id, kid_index),
    CONSTRAINT credential_keys_status_chk CHECK (status IN ('active','invalid')),
    CONSTRAINT credential_keys_kid_pos_chk CHECK (kid_index >= 1)
);

CREATE INDEX IF NOT EXISTS idx_credential_keys_credential
    ON public.credential_keys(credential_id)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_credential_keys_tenant
    ON public.credential_keys(tenant_id);

-- Keep the duplicate tenant_id in sync with its parent credential. A plain FK
-- on credential_id cannot enforce this because credentials.id is the sole PK.
CREATE OR REPLACE FUNCTION public.credential_keys_enforce_parent_tenant()
RETURNS TRIGGER AS $$
DECLARE
    parent_tenant text;
BEGIN
    SELECT tenant_id INTO parent_tenant
    FROM public.credentials
    WHERE id = NEW.credential_id;

    IF parent_tenant IS NULL OR NEW.tenant_id <> parent_tenant THEN
        RAISE EXCEPTION 'credential_keys tenant_id must match parent credential'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_credential_keys_enforce_parent_tenant ON public.credential_keys;
CREATE TRIGGER trg_credential_keys_enforce_parent_tenant
    BEFORE INSERT OR UPDATE OF credential_id, tenant_id ON public.credential_keys
    FOR EACH ROW EXECUTE FUNCTION public.credential_keys_enforce_parent_tenant();

-- updated_at 触发器 (与 free_resource_catalog 同一模式).
CREATE OR REPLACE FUNCTION public.credential_keys_touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_credential_keys_touch_updated_at ON public.credential_keys;
CREATE TRIGGER trg_credential_keys_touch_updated_at
    BEFORE UPDATE ON public.credential_keys
    FOR EACH ROW EXECUTE FUNCTION public.credential_keys_touch_updated_at();

-- RLS: 按 tenant 隔离 (与 credentials 表的 RLS policy 一致).
ALTER TABLE public.credential_keys ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation_credential_keys ON public.credential_keys;
CREATE POLICY tenant_isolation_credential_keys ON public.credential_keys
    USING (
        tenant_id = public.get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    )
    WITH CHECK (
        tenant_id = public.get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );
