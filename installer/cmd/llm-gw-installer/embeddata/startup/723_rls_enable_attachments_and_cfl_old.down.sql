-- 723 down (R40 RLS Phase 1, 2026-09-18): revert to dormant-policies state.
-- R42 (2026-09-18): to_regclass guards — candidate_failure_logs_columnar_old
-- is absent on canonical-chain databases without V359 deploy lineage; see
-- the up-migration header for the failure class.

DO $$
BEGIN
    IF to_regclass('public.attachments') IS NOT NULL THEN
        ALTER TABLE public.attachments NO ROW LEVEL SECURITY;
    ELSE
        RAISE NOTICE '723 down: table public.attachments not present, skipping';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.candidate_failure_logs_columnar_old') IS NOT NULL THEN
        ALTER TABLE public.candidate_failure_logs_columnar_old NO ROW LEVEL SECURITY;
    ELSE
        RAISE NOTICE '723 down: table public.candidate_failure_logs_columnar_old not present, skipping';
    END IF;
END $$;
