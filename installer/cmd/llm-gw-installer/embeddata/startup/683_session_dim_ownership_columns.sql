-- Migration 683: session_dim 补 ownership 归属列(api_key_id/application_*/owner_*/end_user_id/client_id)
--
-- Background (2026-09-07 网关日志审计, 42703 x ~19/min):
--   internal/sessionv2mirror/session_dim.go 的 upsert 引用 api_key_id,
--   application_id, application_code, owner_user, api_key_owner_user,
--   end_user_id, client_id —— 这些列由 358_session_ownership.sql ALTER
--   进 session_dim,但在本库 session_dim 是 350 的 9 列窄形状
--   (gw_session_id/session_key/tenant_id/task_id/status/first_request_at/
--    last_active_at/closed_at/created_at + project_id),358 的 ALTER 从未
--   落地 → sessionv2mirror 每次会话镜像写 42703
--   "column api_key_id of relation session_dim does not exist"。
--
--   不能整文件重放 358:它 CREATE OR REPLACE update_session_summary(),
--   而 572/563/661 是有意维护的函数链(apply-db-revision-sequence.sh
--   clobber guard),重放会用 358 旧函数体覆盖 661 的最终定义。本迁移
--   只取其 session_dim 列 + 索引段,列定义与 358:25-45 完全一致。
--
-- Idempotent: YES(ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS)。

ALTER TABLE session_dim
    ADD COLUMN IF NOT EXISTS api_key_id         BIGINT,
    ADD COLUMN IF NOT EXISTS application_id     BIGINT,
    ADD COLUMN IF NOT EXISTS application_code   VARCHAR(64),
    ADD COLUMN IF NOT EXISTS owner_user         VARCHAR(128),
    ADD COLUMN IF NOT EXISTS api_key_owner_user VARCHAR(128),
    ADD COLUMN IF NOT EXISTS end_user_id        VARCHAR(128),
    ADD COLUMN IF NOT EXISTS client_id          VARCHAR(128);

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
COMMENT ON COLUMN session_dim.client_id IS '接入方身份：COALESCE(application_code, api_key_prefix)，替代 client_models[1] 启发式';
