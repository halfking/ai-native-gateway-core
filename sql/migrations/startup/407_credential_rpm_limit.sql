-- =============================================================================
-- Migration 407: credentials.rpm_limit — per-credential client-side RPM cap
-- Created:     2026-07-15
-- Author:      gateway maintainers (NIM stabilization: free tier overload)
--
-- Root cause:
--   The free-pool template (admin/free_pool_extra.go) defines an rpmLimit
--   per provider (e.g. nvidia-nim-free = 10 RPM) but the value was never
--   persisted to the DB and never enforced. NIM's free tier returns 429
--   ("Too Many Requests") when bursty traffic exceeds the per-account
--   RPM budget; without client-side enforcement we hit the upstream
--   limit, then experience KindRateLimit soft-recovery, oscillation,
--   and observable 429 churn (5+ probe_direct_rate_limited on NIM
--   endless in the last 48h).
--
-- What this migration does:
--   1. Add credentials.rpm_limit integer (NULL = unlimited, 0 = unlimited).
--      NULL/0 paths preserve current behaviour so paid credentials are
--      unaffected. Only credentials with an explicit positive value get
--      client-side throttling.
--   2. Backfill from the free-pool template's hard-coded values for the
--      well-known free providers (nvidia=10, groq=30, etc.). Manual ops
--      can override per-credential after this migration.
--
-- Companion changes (NOT in this migration):
--   - admin/free_pool_extra.go: persist tpl.rpmLimit into INSERT/UPDATE
--     so newly registered free-pool credentials get rpm_limit set
--     automatically.
--   - provider/client.go: load rpm_limit into Candidate.RPMLimit.
--   - domains/credential/limiter.go: enforce via a credential-scoped
--     sliding window inside AcquireAll.
--
-- Idempotent: ADD COLUMN IF NOT EXISTS. UPDATE has WHERE guard so re-run
-- only sets values on rows that still have NULL.
--
-- Rollback: ALTER TABLE credentials DROP COLUMN rpm_limit.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 407 credentials.rpm_limit ==='

-- ---------------------------------------------------------------------------
-- 1. Add the column.
-- ---------------------------------------------------------------------------
ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS rpm_limit integer;

COMMENT ON COLUMN public.credentials.rpm_limit IS
'Client-side requests-per-minute cap enforced by domains/credential/limiter.
NULL/0 = unlimited (default; matches pre-fix behaviour). Positive value
limits per-minute in-flight requests to the credential, causing the
executor to failover to the next candidate when the limit is hit.
Free-pool credentials (admin/free_pool_extra.go) auto-populate this from
the template rpmLimit; paid credentials are typically NULL.';

-- ---------------------------------------------------------------------------
-- 2. Backfill from well-known free-tier providers. These are the same
--    values the in-memory free-pool template defines; we materialise them
--    in the DB so the runtime can read them without crossing the admin
--    package boundary.
-- ---------------------------------------------------------------------------
UPDATE public.credentials c
SET rpm_limit = tpl.rpm
FROM (
    VALUES
        ('nvidia',                   'endless',          10),
        ('nvidia',                   'nvidia-latest',    10),
        ('nvidia',                   'nvidia-build-new', 10),
        ('nvidia',                   'nvidia-build-v2',  10),
        ('groq',                     'groq-free',        30),
        ('llm7',                     'llm7-free',        60),
        ('mistral',                  'mistral-free',     20),
        ('openrouter',               'openrouter-free',  20),
        ('deepinfra',                'deepinfra-free',   20),
        ('cohere',                   'cohere-free',      20),
        ('huggingface',              'huggingface-free', 10)
) AS tpl(code, label, rpm)
JOIN public.providers p ON p.code = tpl.code
WHERE c.provider_id = p.id
  AND c.label = tpl.label
  AND c.rpm_limit IS NULL;

\echo '--- 2. backfill complete (free-pool credentials with rpm_limit set) ---'
SELECT p.code, c.label, c.rpm_limit
FROM public.credentials c
JOIN public.providers p ON p.id = c.provider_id
WHERE c.rpm_limit IS NOT NULL
ORDER BY p.code, c.label;

\echo '=== 407 credentials.rpm_limit: done ==='