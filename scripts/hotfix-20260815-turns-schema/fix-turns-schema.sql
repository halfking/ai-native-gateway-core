-- fix-turns-schema.sql — 修复 245/154 上 turns 会话列表依赖的缺失 schema（2026-08-15）
--
-- 背景：迁移账本记录 431/456/513 已应用，但 431/456 的列不在 public 表上
-- （历史上 430 把 V2 表重复建到 gateway schema，513 又把 gateway schema 整体
-- DROP，导致早期对 gateway.session_turns 的 ALTER 与账本记录脱节）。
-- session_dim（350/358/407）从未在两环境落地。
-- 症状：
--   1. /api/admin/turns/sessions 500（relation "session_dim" does not exist）
--   2. sessionv2mirror 影子写入持续失败（column "attachment_count" ... does not exist）
--      → public.sessions / public.session_turns 一直为空
-- 本脚本全部幂等（IF NOT EXISTS），可重复执行。

BEGIN;

-- ── 1. session_turns：431 附件列 + 456 展示列 ─────────────────────────
ALTER TABLE public.session_turns
  ADD COLUMN IF NOT EXISTS attachment_count INTEGER DEFAULT 0,
  ADD COLUMN IF NOT EXISTS attachment_total_bytes BIGINT DEFAULT 0,
  ADD COLUMN IF NOT EXISTS multimodal_types TEXT[] DEFAULT '{}';

ALTER TABLE public.session_turns
  ADD COLUMN IF NOT EXISTS attempt_no INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS tools JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS title TEXT,
  ADD COLUMN IF NOT EXISTS summary TEXT;

-- ── 2. sessions：456 总结/快照列（title 已存在） ────────────────────────
ALTER TABLE public.sessions
  ADD COLUMN IF NOT EXISTS last_full_request JSONB,
  ADD COLUMN IF NOT EXISTS last_full_response JSONB,
  ADD COLUMN IF NOT EXISTS last_full_payload_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS summary TEXT,
  ADD COLUMN IF NOT EXISTS summary_model TEXT,
  ADD COLUMN IF NOT EXISTS summary_generated_at TIMESTAMPTZ;

-- summary_quality 带 CHECK，单独处理避免与既有列定义冲突
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='sessions' AND column_name='summary_quality'
  ) THEN
    ALTER TABLE public.sessions
      ADD COLUMN summary_quality TEXT
        CHECK (summary_quality IS NULL OR summary_quality IN ('rejected','partial','verified'));
  END IF;
END $$;

-- ── 3. session_dim：350 基础表 + 358 属主列 + 407 project_id ────────────
CREATE TABLE IF NOT EXISTS session_dim (
    gw_session_id VARCHAR(128) PRIMARY KEY,
    session_key   VARCHAR(255) NOT NULL,
    tenant_id     VARCHAR(255) NOT NULL,
    task_id       VARCHAR(128),
    status        VARCHAR(20)  NOT NULL DEFAULT 'active',
    first_request_at TIMESTAMPTZ,
    last_active_at  TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE session_dim
  ADD COLUMN IF NOT EXISTS api_key_id         BIGINT,
  ADD COLUMN IF NOT EXISTS application_id     BIGINT,
  ADD COLUMN IF NOT EXISTS application_code   VARCHAR(64),
  ADD COLUMN IF NOT EXISTS owner_user         VARCHAR(128),
  ADD COLUMN IF NOT EXISTS api_key_owner_user VARCHAR(128),
  ADD COLUMN IF NOT EXISTS end_user_id        VARCHAR(128),
  ADD COLUMN IF NOT EXISTS client_id          VARCHAR(128),
  ADD COLUMN IF NOT EXISTS project_id         TEXT;

CREATE INDEX IF NOT EXISTS idx_session_dim_tenant
  ON session_dim(tenant_id, last_active_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_dim_task
  ON session_dim(tenant_id, task_id) WHERE task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_dim_owner_user
  ON session_dim(tenant_id, owner_user) WHERE owner_user IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_dim_end_user
  ON session_dim(tenant_id, end_user_id) WHERE end_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_dim_client
  ON session_dim(tenant_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_dim_apikey
  ON session_dim(tenant_id, api_key_id) WHERE api_key_id IS NOT NULL;

COMMENT ON COLUMN session_dim.owner_user IS '会话主属主（首个请求的 api_key_owner_user），用于会话归属与用户画像';
COMMENT ON COLUMN session_dim.end_user_id IS '会话首个请求的终端用户ID';
COMMENT ON COLUMN session_dim.client_id IS '接入方身份：COALESCE(application_code, api_key_prefix)';
COMMENT ON COLUMN session_dim.project_id IS '会话级项目维度（gw_project_id），会话按项目归类';

-- RLS 与同库其他会话表保持一致的策略形态（表 owner 读写不受限）
ALTER TABLE session_dim ENABLE ROW LEVEL SECURITY;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='session_dim' AND policyname='session_dim_tenant_isolation') THEN
    CREATE POLICY session_dim_tenant_isolation ON session_dim
      USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='session_dim' AND policyname='session_dim_super_admin_bypass') THEN
    CREATE POLICY session_dim_super_admin_bypass ON session_dim
      USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
      );
  END IF;
END $$;

COMMIT;

-- ── 验证 ────────────────────────────────────────────────────────────────
SELECT 'session_dim ok' WHERE to_regclass('public.session_dim') IS NOT NULL;
