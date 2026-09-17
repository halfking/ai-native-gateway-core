-- 723 (R40 RLS Phase 1, 2026-09-18): Enable RLS on attachments and
-- candidate_failure_logs_columnar_old. Closes RLS design §五 Phase 1 item 3.
--
-- Context: both tables carry tenant_isolation policies in canonical
-- vocabulary (tenant_id = get_current_tenant()), but relrowsecurity=false,
-- so the policies were dormant shelfware. ENABLE (without FORCE) activates
-- policy evaluation for non-owner roles while table owners keep bypassing —
-- zero behavior change in the current superuser era (llm_gateway is
-- SUPERUSER+BYPASSRLS), and the precondition for the Phase 2 role demotion.
--
-- Intentionally NOT FORCE: Phase 3 flips FORCE per-domain after the GUC
-- coverage proof (design §五 Phase 3). Idempotent: ENABLE is a no-op when
-- already enabled.

ALTER TABLE public.attachments ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.candidate_failure_logs_columnar_old ENABLE ROW LEVEL SECURITY;
