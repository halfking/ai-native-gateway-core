-- 2026-06-15-auto-route-mode-realtime-trigger.sql
-- v2.0.1 — 实时 trigger：credential/model 变更时立即刷新 index
--
-- 设计目标：
--   解决"数据要实时更新"的需求——之前 5min refresh 是 best-effort 周期刷新，
--   但新凭据/模型上线应该立即生效，而不是等下一个 bucket。
--
-- 实现方案：
--   1. PostgreSQL LISTEN/NOTIFY — INSERT/UPDATE/DELETE 后 NOTIFY 'auto_route_refresh'
--   2. Go gateway 监听 channel — 收到通知后 5s debounce 触发 RefreshOnce
--
-- 触发场景：
--   - credential_model_bindings INSERT/UPDATE/DELETE
--   - model_offers (through view) 变更
--   - credentials 凭据健康状态变更
--   - api_keys rate_limit / budget 变更

BEGIN;

-- ============================================================================
-- (a) NOTIFY trigger — INSERT
-- ============================================================================
CREATE OR REPLACE FUNCTION notify_auto_route_refresh()
RETURNS TRIGGER AS $$
BEGIN
    -- 通知 gateway 立即刷新 index（5s debounce 由 Go 端控制）
    PERFORM pg_notify('auto_route_refresh',
        TG_TABLE_NAME || ':' || TG_OP ||
        COALESCE(NEW.credential_id::text, OLD.credential_id::text, ''));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- (b) 在关键表上挂 trigger
-- ============================================================================

-- credential_model_bindings — 凭据-模型绑定变化
DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb ON credential_model_bindings;
CREATE TRIGGER trg_notify_auto_route_cmb
AFTER INSERT OR UPDATE OR DELETE ON credential_model_bindings
FOR EACH ROW
EXECUTE FUNCTION notify_auto_route_refresh();

-- credentials — 凭据健康状态变化
DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON credentials;
CREATE TRIGGER trg_notify_auto_route_creds
AFTER UPDATE OF
    status, availability_state, quota_state, circuit_state,
    concurrency_limit, lifecycle_status
ON credentials
FOR EACH ROW
WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION notify_auto_route_refresh();

-- api_keys — 限流/预算变化（影响推荐指数中的 pressure）
DROP TRIGGER IF EXISTS trg_notify_auto_route_apikeys ON api_keys;
CREATE TRIGGER trg_notify_auto_route_apikeys
AFTER UPDATE OF
    rate_limit_rpm, budget_usd, enabled, status
ON api_keys
FOR EACH ROW
WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION notify_auto_route_refresh();

-- Note: model_offers is a VIEW, not a table — cannot have triggers.
-- Price changes propagate via credentials / credential_model_bindings
-- triggers instead (covers the same change paths).

COMMIT;