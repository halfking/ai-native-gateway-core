-- 723 down (R40 RLS Phase 1, 2026-09-18): revert to dormant-policies state.

ALTER TABLE public.attachments NO ROW LEVEL SECURITY;

ALTER TABLE public.candidate_failure_logs_columnar_old NO ROW LEVEL SECURITY;
