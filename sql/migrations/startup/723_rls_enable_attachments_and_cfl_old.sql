-- 723 (R40 RLS Phase 1, 2026-09-18): Enable RLS on attachments and
-- candidate_failure_logs_columnar_old. Closes RLS design §五 Phase 1 item 3.
--
-- Context: both tables carry tenant_isolation policies in canonical
-- vocabulary (tenant_id = get_current_tenant()). On canonical-lineage
-- databases relrowsecurity=false makes the policies dormant shelfware;
-- deploy-chain (V359) lineage databases may already have RLS enabled via
-- the pre-rename 052 grant — ENABLE is idempotent either way. ENABLE
-- (without FORCE) activates policy evaluation for non-owner roles while
-- table owners keep bypassing — zero behavior change in the current
-- superuser era (llm_gateway is SUPERUSER+BYPASSRLS), and the precondition
-- for the Phase 2 role demotion.
--
-- 2026-09-18 fix (R42 audit, fresh-install / canonical-chain blocker):
-- candidate_failure_logs_columnar_old has no creator in the canonical
-- startup chain or 01-schema — it only exists on deploy-chain (V359
-- conditional rename) lineage databases. The bare ALTER aborted the whole
-- migration with 42P01 (relation does not exist) on installer fresh
-- installs and on canonical-chain databases without deploy lineage, same
-- failure class 720 fixed for its own inventory (see 720 header). Each
-- statement is now wrapped in a per-table to_regclass guard: absent tables
-- are skipped with a NOTICE, present tables get the ENABLE.
--
-- Intentionally NOT FORCE: Phase 3 flips FORCE per-domain after the GUC
-- coverage proof (design §五 Phase 3). Idempotent: ENABLE is a no-op when
-- already enabled.

DO $$
BEGIN
    IF to_regclass('public.attachments') IS NOT NULL THEN
        ALTER TABLE public.attachments ENABLE ROW LEVEL SECURITY;
    ELSE
        RAISE NOTICE '723: table public.attachments not present, skipping ENABLE ROW LEVEL SECURITY';
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.candidate_failure_logs_columnar_old') IS NOT NULL THEN
        ALTER TABLE public.candidate_failure_logs_columnar_old ENABLE ROW LEVEL SECURITY;
    ELSE
        RAISE NOTICE '723: table public.candidate_failure_logs_columnar_old not present, skipping ENABLE ROW LEVEL SECURITY';
    END IF;
END $$;
