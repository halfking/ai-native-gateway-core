-- Migration 397: proxy region avoidance + auto-switch selection policy
-- Adds banned_regions columns and persists the Manager-level selection policy.

ALTER TABLE proxy_subscriptions
    ADD COLUMN IF NOT EXISTS banned_regions TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE proxy_nodes
    ADD COLUMN IF NOT EXISTS banned_regions TEXT[] NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS proxy_selection_policy (
    id                      INTEGER     PRIMARY KEY,
    load_balance_strategy   VARCHAR(32) NOT NULL DEFAULT 'best_only',
    location_affinity       VARCHAR(32) NOT NULL DEFAULT 'any',
    auto_disable_threshold  INTEGER     NOT NULL DEFAULT 3,
    auto_disable_enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    auto_recover_enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    swap_check_interval_ms  INTEGER     NOT NULL DEFAULT 30000,
    swap_failure_threshold  INTEGER     NOT NULL DEFAULT 2,
    updated_at              TIMESTAMP   NOT NULL DEFAULT NOW()
);

-- 单行表（id=1 是唯一一行），gateway 启动时若不存在则插入默认值。
INSERT INTO proxy_selection_policy (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;

COMMENT ON COLUMN proxy_subscriptions.banned_regions IS '订阅层禁用的地区码集合（如 {US,JP}），子节点优先继承';
COMMENT ON COLUMN proxy_nodes.banned_regions IS '节点层禁用的地区码集合（可覆盖订阅层）；空表示不额外禁用';
COMMENT ON TABLE proxy_selection_policy IS '全局代理选择策略（负载均衡、亲和性、禁用阈值、主动切流）';
