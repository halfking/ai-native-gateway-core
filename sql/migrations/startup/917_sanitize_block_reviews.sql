-- 917: 输出策略拦截审核与放行的持久化（2026-10-29 拦截持久化轮）。
--
-- # 背景
--
-- r1039（2026-10-20）给拦截加了客户端可见的规则/原因，第 33 增量（2026-10-28）
-- 加了拦截审核队列与运行时硬阻断开关——但**队列与放行表都是进程内的**，
-- 网关重启即清空：审核批准的放行在重启后失效（同一误报反复打断同一用户），
-- 触发记录也无法回溯「这台网关历史上拦过什么」。
--
-- # 本迁移
--
--   sanitize_block_reviews     拦截审核记录（每次 output/egress 硬阻断一条，
--                              含客户端类型、规则名、匹配说明、载荷摘要——
--                              **不存载荷原文**，摘要不可逆）。
--   sanitize_egress_releases   放行表持久层（进程内存 atomic 表的落库副本；
--                              审核批准写入 source='review'、expires_at=+7天，
--                              重启后由启动装载恢复）。
--   sanitize_hardblock_state   硬阻断清单的管理面持久层（单例行；后台保存的
--                              清单重启后仍生效，env 只是没有管理面覆盖时的
--                              出厂默认）。
--
-- 全部幂等（CREATE TABLE/INDEX IF NOT EXISTS），纯新增、不改既有对象。

-- 锁守卫（startup_ddl_lock_guard_gate_test.go）：本文件含 CREATE INDEX（非 CONCURRENTLY，
-- 拿 ShareLock）⇒ 必须自带 lock_timeout，避免等锁期间堵住全库（参见 §44；912/913
-- 补锁守卫同款 —— 999d1c9e6）。幂等 DDL 本身在锁等待前就会跳过已存在对象。
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

CREATE TABLE IF NOT EXISTS sanitize_block_reviews (
 id BIGSERIAL PRIMARY KEY,
 tenant_id TEXT NOT NULL DEFAULT '',
 client_type TEXT NOT NULL DEFAULT '',
 scope TEXT NOT NULL DEFAULT 'egress',
 rule_id TEXT NOT NULL,
 rule_name TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 field_path TEXT NOT NULL DEFAULT '',
 payload_digest TEXT NOT NULL DEFAULT '',
 request_id TEXT NOT NULL DEFAULT '',
 match_detail TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 decided_by TEXT NOT NULL DEFAULT '',
 decided_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS sanitize_block_reviews_status_created_idx
 ON sanitize_block_reviews(status, created_at DESC);
CREATE INDEX IF NOT EXISTS sanitize_block_reviews_tenant_created_idx
 ON sanitize_block_reviews(tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS sanitize_egress_releases (
 id BIGSERIAL PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 scope TEXT NOT NULL DEFAULT 'egress',
 rule_id TEXT NOT NULL,
 payload_digest TEXT NOT NULL DEFAULT '',
 client_type TEXT NOT NULL DEFAULT '',
 approved_by TEXT NOT NULL DEFAULT '',
 source TEXT NOT NULL DEFAULT 'manual' CHECK(source IN ('manual','review')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 expires_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS sanitize_egress_releases_key_idx
 ON sanitize_egress_releases(tenant_id, scope, rule_id, payload_digest);

CREATE TABLE IF NOT EXISTS sanitize_hardblock_state (
 singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(singleton),
 rules JSONB NOT NULL DEFAULT '[]'::jsonb,
 updated_by TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO sanitize_hardblock_state(singleton) VALUES(TRUE) ON CONFLICT DO NOTHING;
