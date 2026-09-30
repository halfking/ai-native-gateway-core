-- ===========================================================================
-- File:          sql/migrations/startup/802_session_turn_details_gw_task_id_index.sql
-- Migration:     802
-- Database:      llm_gateway
-- Purpose:       session_turn_details 族补 (tenant_id, gw_task_id) 部分索引
--                （母表 + hot），供跨租户访问门 assertTaskInTenant 的 session
--                族腿走索引
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX IF NOT EXISTS ×2 + COMMENT ON 覆盖式)
-- Dependencies:  733_session_turn_details.sql（建表与分区）
--                801_session_turn_details_duplicate_drain.sql（同族前置）
--
-- Background:
--   2026-09-30 会话存储解耦 v3 S4 前置。S4 停写后 request_logs 不可依赖，
--   缺此索引时 assertTaskInTenant 的 EXISTS 判定会对 167 万行母表做顺序
--   扫描。位置约束同 StartupFiles：必须晚于 733 与 801，故排在序列末尾。
--
--   2026-10-01 R35-N1 批判复审轮注：本文件曾由迁移双轨制死区脱档——真库
--   已手工 psql 应用（索引/注释在位），文件本体未随序列登记提交，堵住后续
--   部署的 missing-migration 预检。本文件按真库 pg_indexes.indexdef 与
--   obj_description 逐字回填（批判复审轮代补齐，如与 S4 原稿冲突以原稿为准）。
-- ===========================================================================

CREATE INDEX IF NOT EXISTS idx_session_turn_details_tenant_gw_task_id
    ON ONLY public.session_turn_details USING btree (tenant_id, gw_task_id)
    WHERE gw_task_id IS NOT NULL;

COMMENT ON INDEX idx_session_turn_details_tenant_gw_task_id IS
    '802: 跨租户访问门 assertTaskInTenant 的 session 族母表腿。S4 停写后 request_logs 不可依赖，缺此索引会让 EXISTS 判定退化为 167 万行顺序扫描。';

CREATE INDEX IF NOT EXISTS idx_session_turn_details_hot_tenant_gw_task_id
    ON public.session_turn_details_hot USING btree (tenant_id, gw_task_id)
    WHERE gw_task_id IS NOT NULL;

COMMENT ON INDEX idx_session_turn_details_hot_tenant_gw_task_id IS
    '802: 与母表同形态，供未 promote 的近期行走索引（对齐 525/526 的 hot 腿惯例）。';
