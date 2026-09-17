-- 725 (R41 P1-1/P1-2 前置, 2026-09-18): add super_admin_bypass policy to
-- request_logs (P1-1) and tenant_model_policies (P1-2 ReloadAll 旁路).
-- Closes RLS design §五 Phase 2 前置缺口。
--
-- Context: R41 audit found both tables' policies lacked the standard bypass
-- branch (current_role='super_admin' OR bypass_rls='true'). With FORCE+
-- ENABLE active (both confirmed relforcerowsecurity=t on local llm_gateway
-- 真库), the post-降权 shape (NOSUPERUSER owner) means:
--   - request_logs: promote/administration/worker 全仓双 GUC 通道
--     (withAllTenantTx 等) 对本表失效 → promote 批次遇非 default 行 → WITH
--     CHECK 42501 整批失败；admin 全租户态静默缩水（401,624 行潜伏）
--   - tenant_model_policies: ReloadAll 裸池查 SELECT 无 GUC → 跨租户拉取
--     静默 0 行；新增 super_admin_bypass 后 checker 可走 SET LOCAL
--     app.current_role='super_admin' 旁路（替代原 SET LOCAL row_security=off，
--     后者在 NOSUPERUSER owner 下被 PG 在 SELECT 阶段抛 ERROR，已由 R41
--     staging 矩阵 B/F 实证）
--
-- 同样不影响 superuser 期行为：bypass 分支对 SUPERUSER+BYPASSRLS 角色是
-- 多余但与既有 super_admin_bypass 系列同形（analysis_events_322、
-- session_bodies_430、output_compliance_316 等），无回归。
--
-- 全部语句幂等（DROP POLICY IF EXISTS + CREATE POLICY）；不修既有 policy
-- 形态——R40 §五 V720 policy 词汇统一已收口 canonical 形态不动。

-- ── request_logs（P1-1）──

DROP POLICY IF EXISTS request_logs_super_admin_bypass ON public.request_logs;
CREATE POLICY request_logs_super_admin_bypass ON public.request_logs
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

-- ── tenant_model_policies（P1-2 ReloadAll）──

DROP POLICY IF EXISTS tenant_model_policies_super_admin_bypass ON public.tenant_model_policies;
CREATE POLICY tenant_model_policies_super_admin_bypass ON public.tenant_model_policies
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');