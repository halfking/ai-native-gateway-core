-- Migration 036: Add fp_slot_limit column to credentials
--
-- Why: The fingerprint slot pool (used for stable virtual identity
-- disguise against upstream rate-limits) is conceptually independent
-- from concurrency_limit:
--   - concurrency_limit = how many in-flight REQUESTS this credential
--     can handle at once (managed by Limiter package)
--   - fp_slot_limit = how many distinct USER IDENTITIES this credential
--     can simulate (managed by credentialfpslot package)
--
-- Previously EffectiveLimit() in credentialfpslot was reading
-- concurrency_limit and using it as the fp slot pool size, conflating
-- the two concepts. Adding this column gives each credential an
-- independent fingerprint pool size.
--
-- Default value 20 (bumped from 5 on 2026-06-24) matches the Go
-- credentialfpslot.DefaultDefaultLimit. Credentials that already had
-- concurrency_limit set keep their existing behavior untouched —
-- we only ADD a new column with its own default.
ALTER TABLE credentials
ADD COLUMN IF NOT EXISTS fp_slot_limit INT;

-- Backfill: for existing credentials, default fp_slot_limit to 20
-- (bumped from 5 on 2026-06-24; matches Go DefaultDefaultLimit).
UPDATE credentials
SET fp_slot_limit = 20
WHERE fp_slot_limit IS NULL;

-- Make it NOT NULL after backfill so future inserts must specify it
ALTER TABLE credentials
ALTER COLUMN fp_slot_limit SET NOT NULL;

-- Add a CHECK constraint to ensure 0 = unlimited, >0 = explicit pool size.
-- This prevents accidental negative or absurdly large values.
ALTER TABLE credentials
ADD CONSTRAINT credentials_fp_slot_limit_check
CHECK (fp_slot_limit >= 0 AND fp_slot_limit <= 10000);

COMMENT ON COLUMN credentials.fp_slot_limit IS
'Fingerprint slot pool size: number of distinct virtual user identities this credential can simulate. 0 = unlimited. Distinct from concurrency_limit which controls in-flight request count.';

-- Also add a global setting for total end-user count cap
-- (separate table to avoid bloating credentials)
CREATE TABLE IF NOT EXISTS system_identity_pool (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    max_identities INT NOT NULL DEFAULT 10000,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT
);

COMMENT ON TABLE system_identity_pool IS
'Global cap on total distinct end-user identities the gateway will accept. Once this many unique fingerprints are active, new connections must reuse an existing fingerprint (round-robin among least-recently-used).';

-- Backfill: ensure the singleton row exists
INSERT INTO system_identity_pool (id, max_identities)
VALUES (1, 10000)
ON CONFLICT (id) DO NOTHING;