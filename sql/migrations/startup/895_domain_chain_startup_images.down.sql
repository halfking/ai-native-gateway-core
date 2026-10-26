-- ===========================================================================
-- File:          sql/migrations/startup/895_domain_chain_startup_images.down.sql
-- Migration:     895 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 895 —— 删掉它建的 11 张 domain 链表镜像与 6 个触发器函数。
--
-- ⚠⚠ **回滚会销毁这 11 张表里的数据** ⇒ 回滚前请确认这些数据可丢
--   （或先自行导出）。
--   ⇒ 但在生产上（252 共享库 / 211）这 11 张表**从未存在过**
--     ⇒ ★ **若 895 尚未在生产应用过，本 down 是完全无损的**
--       （只会把「本来就没有」的东西再确认一遍没有）。
--
-- ── 删哪些、为什么不删哪些 ─────────────────────────────────────────────
--
--   删（895 建的对象）：
--     · 11 张表：client_profiles / client_behavior_events /
--                credential_state_nodes / feishu_bot_routing_rules /
--                feishu_bot_send_log / notification_channels /
--                provider_incidents / provider_reputation_timeseries /
--                session_credential_rotations / session_state_snapshots /
--                tool_executions
--     · 6 个触发器函数（表被删时，挂在表上的触发器随之消失 ⇒
--       这 6 个函数必须显式 DROP，否则会留下**指向已删表的函数**）：
--       update_client_profile_updated_at / update_feishu_route_updated_at /
--       update_notification_channels_updated_at /
--       update_provider_reputation_updated_at / calculate_incident_duration /
--       calculate_tool_execution_duration
--
--   ★ **不删**：
--     · `public.credentials` —— 895 只**引用**它（外键目标），没有创建它。
--     · `tool_usage_stats` / `approval_routing_rules` / `credential_state_log`
--       —— 这三张表**由 baseline 建**，不是 895 建的（895 的文件头已说明）
--       ⇒ **删它们会毁掉 baseline 的对象** ⇒ 绝不在此删。
--     · `notification_send_log` —— 895 没有建它。
--
-- ── 幂等：IF EXISTS（表 / 函数 / 触发器都是）。
--   ⇒ 本 down 可重复执行。
--
-- ⚠ 顺序：先删表（连带删触发器）⇒ 再删函数。
--   反过来做的话，`DROP FUNCTION` 会被仍挂在表上的触发器依赖挡住
--   （PostgreSQL 会报 2BP01 dependent objects still exist）。
-- ===========================================================================

BEGIN;

-- 与 895 正文同一套超时预算（见 895 文件头的「超时预算」一节）。
SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '300s';

-- ── ① 先删表（连带删掉挂在它们身上的 7 个触发器）─────────────────────
DROP TABLE IF EXISTS public.client_profiles;
DROP TABLE IF EXISTS public.client_behavior_events;
DROP TABLE IF EXISTS public.credential_state_nodes;
DROP TABLE IF EXISTS public.feishu_bot_routing_rules;
DROP TABLE IF EXISTS public.feishu_bot_send_log;
DROP TABLE IF EXISTS public.notification_channels;
DROP TABLE IF EXISTS public.provider_reputation_timeseries;
DROP TABLE IF EXISTS public.provider_incidents;
DROP TABLE IF EXISTS public.session_state_snapshots;
DROP TABLE IF EXISTS public.session_credential_rotations;
DROP TABLE IF EXISTS public.tool_executions;

-- ── ② 再删 6 个触发器函数（此时已无触发器依赖它们）────────────────────
DROP FUNCTION IF EXISTS public.update_client_profile_updated_at();
DROP FUNCTION IF EXISTS public.update_feishu_route_updated_at();
DROP FUNCTION IF EXISTS public.update_notification_channels_updated_at();
DROP FUNCTION IF EXISTS public.update_provider_reputation_updated_at();
DROP FUNCTION IF EXISTS public.calculate_incident_duration();
DROP FUNCTION IF EXISTS public.calculate_tool_execution_duration();

COMMIT;
