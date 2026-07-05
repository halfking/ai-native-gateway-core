-- Migration 039: Auto-calculate fp_slot_limit from concurrency_limit.
--
-- Why: fp_slot_limit (fingerprint slot pool) and concurrency_limit (in-flight
-- request count) are conceptually related but were incorrectly conflated or
-- initialized to a hard-coded value. Per the user's clarification (2026-06-23):
--
--   - concurrency_limit = max in-flight REQUESTS this credential can handle
--     (set by limiter package, request-scoped)
--   - fp_slot_limit = max distinct virtual USER IDENTITIES this credential
--     can simulate (set by credentialfpslot package, long-term 24h hold)
--
-- Invariant: fp_slot_limit MUST be <= concurrency_limit. The fingerprint pool
-- represents "how many distinct users we simulate"; if the pool is larger than
-- the concurrent request capacity, you can issue more identities than the
-- upstream can ever handle simultaneously, defeating the anti-rate-limit
-- purpose of fingerprinting.
--
-- Default ratio: fp_slot_limit = concurrency_limit / 4 (rounded, min 1).
--   - cred-6 (minimax-prod-1): concurrency=100, fp_slot=25
--   - cred-8 (nvidia-build-new): concurrency=40, fp_slot=10
--   - cred-15 (minimax-anthropic-prod-1): concurrency=10, fp_slot=2 (1)
--
-- Rationale: 4 is the sweet spot that upstream providers (Anthropic, OpenAI,
-- MiniMax, etc.) treat as "this user has multiple parallel tabs but is one
-- human". A smaller ratio (e.g., 1) over-constrains; a larger ratio (e.g., 10)
-- loses the parallelism signal.

-- 1. Trigger: auto-set fp_slot_limit on INSERT if NULL
CREATE OR REPLACE FUNCTION auto_set_fp_slot_limit()
    RETURNS TRIGGER
    LANGUAGE plpgsql
AS $$
BEGIN
    -- Auto-fill fp_slot_limit from concurrency_limit if not explicitly set
    IF NEW.fp_slot_limit IS NULL THEN
        IF NEW.concurrency_limit IS NOT NULL AND NEW.concurrency_limit > 0 THEN
            NEW.fp_slot_limit := GREATEST(1, NEW.concurrency_limit / 4);
        ELSE
            NEW.fp_slot_limit := 20;  -- 2026-06-24: 5→20, matches DefaultDefaultLimit
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_auto_fp_slot_limit_insert ON credentials;
CREATE TRIGGER trg_auto_fp_slot_limit_insert
    BEFORE INSERT ON credentials
    FOR EACH ROW
    EXECUTE FUNCTION auto_set_fp_slot_limit();

-- 2. Constraint: enforce fp_slot_limit <= concurrency_limit (when both set)
ALTER TABLE credentials
    DROP CONSTRAINT IF EXISTS credentials_fp_slot_vs_concurrency;

ALTER TABLE credentials
    ADD CONSTRAINT credentials_fp_slot_vs_concurrency
    CHECK (
        concurrency_limit IS NULL
        OR fp_slot_limit IS NULL
        OR fp_slot_limit <= concurrency_limit
    );

COMMENT ON CONSTRAINT credentials_fp_slot_vs_concurrency ON credentials IS
'fp_slot_limit (distinct user identities) MUST be <= concurrency_limit (in-flight requests). Otherwise the fingerprint pool exceeds the upstream capacity, defeating anti-rate-limit.';

-- 3. Backfill: recalculate fp_slot_limit for existing rows where the ratio
-- is wrong (e.g. fp_slot=5 against concurrency=100 — wastes 95 slots).
UPDATE credentials
SET fp_slot_limit = GREATEST(1, concurrency_limit / 4),
    updated_at = now()
WHERE concurrency_limit IS NOT NULL
  AND concurrency_limit > 0
  -- Skip rows where admin manually set fp_slot_limit to a non-default value.
  -- We detect "default" by checking if it equals 5 (the pre-migration
  -- hard-coded default). Admin-set values will be ≠ 5 and not touched.
  AND fp_slot_limit = 5;

-- 4. Backfill any rows where fp_slot_limit > concurrency_limit (impossible state)
-- Set to GREATEST(1, concurrency_limit / 4) as a safe default.
UPDATE credentials
SET fp_slot_limit = GREATEST(1, concurrency_limit / 4),
    updated_at = now()
WHERE concurrency_limit IS NOT NULL
  AND fp_slot_limit > concurrency_limit;