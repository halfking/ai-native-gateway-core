-- 725 down (R41 P1-1/P1-2 前置, 2026-09-18): revert to single-policy shape.
--
-- Drops the two super_admin_bypass policies added by 725_up. Drops the
-- bypass branch back off request_logs and tenant_model_policies, restoring
-- the pre-R41 R41-audit state where both tables were exposed to the
-- post-降权 FORCE ERROR. Use only as an emergency escape hatch during the
-- Phase 2 role demotion; long-term rollback of the bypass re-creates the
-- R41 P1-1/P1-2 risk profile.

DROP POLICY IF EXISTS request_logs_super_admin_bypass ON public.request_logs;
DROP POLICY IF EXISTS tenant_model_policies_super_admin_bypass ON public.tenant_model_policies;