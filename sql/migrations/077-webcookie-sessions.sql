-- 077-webcookie-sessions.sql
-- 持久化 web-cookie 逆向 provider 的浏览器会话 (cookies / session token).
--
-- 背景: web-cookie provider (chatgpt-web, claude-web, deepseek-web 等) 通过
-- 逆向浏览器聊天接口提供免费 LLM 访问. 它们需要有效的浏览器会话 (cookies +
-- CSRF token), 由 domains/streaming/executors/webcookie 框架管理.
-- 本表持久化这些会话, 让多实例共享 + 重启不丢失登录态.
--
-- 状态 (2026-08-10): 框架 + deepseek-web 骨架已就绪, 其余 provider 待逐站抓包.
-- 会话表先建好, executor 实现后即可读写.

CREATE TABLE IF NOT EXISTS public.webcookie_sessions (
    id              BIGSERIAL    PRIMARY KEY,
    provider_code   TEXT         NOT NULL,
    account_label   TEXT         NOT NULL DEFAULT 'default',
    cookies_json    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    session_meta    JSONB        NOT NULL DEFAULT '{}'::jsonb,  -- x-vqd-4 token, csrf 等
    status          TEXT         NOT NULL DEFAULT 'active',     -- active | expired | banned | refreshing
    refreshed_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    consecutive_failures INT     NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    tenant_id       TEXT         NOT NULL DEFAULT 'default',
    CONSTRAINT webcookie_sessions_provider_account_key UNIQUE (provider_code, account_label, tenant_id),
    CONSTRAINT webcookie_sessions_status_chk CHECK (status IN ('active','expired','banned','refreshing'))
);

CREATE INDEX IF NOT EXISTS idx_webcookie_sessions_provider
    ON public.webcookie_sessions(provider_code)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_webcookie_sessions_tenant
    ON public.webcookie_sessions(tenant_id);

-- updated_at 触发器.
CREATE OR REPLACE FUNCTION public.webcookie_sessions_touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_webcookie_sessions_touch_updated_at ON public.webcookie_sessions;
CREATE TRIGGER trg_webcookie_sessions_touch_updated_at
    BEFORE UPDATE ON public.webcookie_sessions
    FOR EACH ROW EXECUTE FUNCTION public.webcookie_sessions_touch_updated_at();

ALTER TABLE public.webcookie_sessions ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation_webcookie_sessions ON public.webcookie_sessions;
CREATE POLICY tenant_isolation_webcookie_sessions ON public.webcookie_sessions
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
