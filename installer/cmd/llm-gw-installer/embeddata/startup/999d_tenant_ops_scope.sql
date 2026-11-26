-- Migration: 999d_tenant_ops_scope
-- Purpose: 给 licenses 与 releases 补 tenant_id 列 ＋ 两个查询索引 ——
--          让 tenantops 的两个租户运维接口（license 状态／autoupdate 检查）真的能用
-- Phase: 第 31 轮（存量缺口收口轮 · 2026-11-26）
-- Author: huangxt
-- Date: 2026-11-26
--
-- ★ 背景：★ 「生产 SQL 引用的列必须有投递来源创建」门（internal/schemadelivery，
--   2026-11-26 第 30 轮新立）第一次校准就抓到了这条：tenantops/handler.go 的两个
--   处理器读 licenses.tenant_id 与 releases.tenant_id ⇒ 而创建这两列的文件
--   （sql/migrations/startup/up/389_tenant_ops_scope.sql）**不在任何投递通道里**。
--
-- ★ 为什么它没被投递（★ 这是个目录级的陷阱，不是个别遗漏）：
--   sql/migrations/startup/up/ 这个目录里有 13 个件（376/377/378/379/388/389/390/
--   410/411/600/632 …）—— 它们的编号**都已被 startup 根层的另一批件占用**
--   （例：410_ip_blocklist 在根层与 up/ 各有一份；根层那份成对带 .down.sql 且在通道里，
--   up/ 那份没有）⇒ up/ 是**已被根层取代的旧件区**，从来不被任何通道消费。
--   ⇒ 这不是「少登记了一个件」，而是「文件放在了一个没人消费的目录」。
--
-- ★ 为什么不能靠「改代码避开 tenant_id」（★ 那会削弱租户隔离）：
--   这两个读面带的是**租户隔离语义**（licenses: WHERE tenant_id = $1；
--   releases: WHERE tenant_id = $1 OR tenant_id = 'default'）⇒ 去掉它等于让所有租户
--   看到同一份平台级数据 ⇒ owner 2026-11-26 决定：**投递这一列**，不改读面语义。
--
-- ★ 这两个接口已经接线（cmd/gateway/main.go 里注册了
--   GET /api/tenant/license/status 与 GET /api/tenant/autoupdate/check）
--   ⇒ 生产上它们现在返回 500（42703 被 handler 包成内部错误）⇒ 本迁移让它们能用。
--
-- ★ 与 389 的关系：★ 本迁移是 389 的**投递版**（内容等价、位置改为通道内、
--   编号另取 999d）—— 389 那份留在原处不动（它在没人消费的目录里，等于没生效；
--   ★ 若将来有人把 up/ 目录接进通道，那就会与本迁移重复 ⇒ 届时删 389）。
-- ★ 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go：版本 > 870 且含强锁 DDL
--   ⇒ 必须自带 lock_timeout）：本件含 ALTER TABLE ＋ CREATE INDEX ⇒ 声明
--   5s lock_timeout / 10min statement_timeout（同 999a 口径）。

\set ON_ERROR_STOP on

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

ALTER TABLE public.licenses
    ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';

ALTER TABLE public.releases
    ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';

CREATE INDEX IF NOT EXISTS idx_licenses_tenant_created
    ON public.licenses (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_releases_tenant_published
    ON public.releases (tenant_id, published_at, build_seq DESC);

COMMENT ON COLUMN public.licenses.tenant_id IS
    '租户隔离键；平台级存量记录为 default（2026-11-26 随 999d 投递）';
COMMENT ON COLUMN public.releases.tenant_id IS
    '租户隔离键；平台级存量记录为 default（2026-11-26 随 999d 投递）';

COMMIT;
