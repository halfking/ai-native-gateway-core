-- =============================================================================
-- Migration 394: NVIDIA NIM outbound_model_name canonicalization
-- Created:     2026-07-14
-- Author:      gateway maintainers (NIM model_not_found incident)
--
-- Root cause:
--   integrate.api.nvidia.com requires publisher-prefixed model ids:
--     z-ai/glm-5.2
--     minimaxai/minimax-m3
--     minimaxai/minimax-m2.7
--   Bare short names ("glm-5.2", "minimax-m3", "minimax-m2.7") cause
--   upstream 404 / model_not_found responses.
--
--   Historical provider_models rows on NVIDIA provider(s) accumulated
--   drift (e.g. raw_model_name = 'z-ai/glm-5.2' but outbound_model_name
--   = 'glm-5.1'). See sql/fixes/fix-glm52-alias-drift.sql for the GLM-5.2
--   portion of the same root cause (already addressed for zhipu /
--   sensenova / scnet / glm-xianyu / glm-5.2-oneday). This migration
--   does the equivalent for the NVIDIA provider and extends coverage to
--   MiniMax-M3 and MiniMax-M2.7.
--
-- What this migration does:
--   For every provider_models row whose provider code = 'nvidia' AND whose
--   raw_model_name is one of the three known-good NIM publisher ids, set
--   outbound_model_name to the same value (idempotent, NULL-safe). The
--   COALESCE fallback in provider/client.go:749 already covers raw-only
--   rows, but pin the column to remove any chance of drift through
--   future operator edits.
--
-- What this migration does NOT do:
--   - It does not touch user-managed third-party providers that happen to
--     share the code 'nvidia' (none in the default seed).
--   - It does not modify models_canonical.canonical_name. Clients can
--     continue to use the short names ("glm-5.2", "minimax-m3",
--     "minimax-m2.7"); the gateway resolves them via the offer raw name.
--   - It does not insert new provider_models rows. New rows arrive via
--     discovery/discovery.go upserting from NVIDIA /v1/models.
--
-- Pre-flight (read-only) blocks help operators inspect affected rows
-- before/after running the DML. The DML block is commented out so that
-- the script is safe to run as a normal startup migration.
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 394 NIM outbound_model_name canonicalization ==='

-- ---------------------------------------------------------------------------
-- 0. Sanity: confirm provider 'nvidia' exists.
-- ---------------------------------------------------------------------------
SELECT id, code, base_url
FROM providers
WHERE code = 'nvidia';

-- ---------------------------------------------------------------------------
-- 1. Pre-flight: list every NVIDIA provider_models row whose
--    outbound_model_name currently does NOT match the canonical NIM id.
-- ---------------------------------------------------------------------------
\echo '--- 1. NVIDIA provider_models rows with non-canonical outbound_model_name ---'
SELECT
    pm.id,
    pm.provider_id,
    pm.raw_model_name,
    pm.standardized_name,
    pm.outbound_model_name,
    pm.available,
    pm.updated_at
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.code = 'nvidia'
  AND pm.raw_model_name IN (
      'z-ai/glm-5.2',
      'minimaxai/minimax-m3',
      'minimaxai/minimax-m2.7'
  )
  AND (
      pm.outbound_model_name IS NULL
      OR pm.outbound_model_name <> pm.raw_model_name
  )
ORDER BY pm.id;

-- ---------------------------------------------------------------------------
-- 2. Apply: pin outbound_model_name to the raw name for the three target
--    rows. Idempotent and NULL-safe.
--
--    Removed-walrus form intentionally omitted: we do not want to clobber
--    an admin override (the three target ids are exactly what NVIDIA NIM
--    accepts today per https://integrate.api.nvidia.com/v1/models, so
--    an admin override here would only ever be wrong).
-- ---------------------------------------------------------------------------

UPDATE provider_models pm
SET outbound_model_name = pm.raw_model_name,
    updated_at = NOW()
FROM providers p
WHERE p.id = pm.provider_id
  AND p.code = 'nvidia'
  AND pm.raw_model_name IN (
      'z-ai/glm-5.2',
      'minimaxai/minimax-m3',
      'minimaxai/minimax-m2.7'
  )
  AND (
      pm.outbound_model_name IS NULL
      OR pm.outbound_model_name <> pm.raw_model_name
  );

-- ---------------------------------------------------------------------------
-- 3. Post-flight: confirm every NVIDIA target row is canonical now.
--    The query should return 0 rows.
-- ---------------------------------------------------------------------------
\echo '--- 3. After-fix drift (expect 0 rows) ---'
SELECT
    pm.id,
    pm.raw_model_name,
    pm.outbound_model_name
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.code = 'nvidia'
  AND pm.raw_model_name IN (
      'z-ai/glm-5.2',
      'minimaxai/minimax-m3',
      'minimaxai/minimax-m2.7'
  )
  AND (
      pm.outbound_model_name IS NULL
      OR pm.outbound_model_name <> pm.raw_model_name
  );

-- ---------------------------------------------------------------------------
-- 4. Final snapshot for the operator: the three target rows post-fix.
-- ---------------------------------------------------------------------------
\echo '--- 4. Final NVIDIA target rows (z-ai/glm-5.2 / minimaxai/minimax-m3 / minimaxai/minimax-m2.7) ---'
SELECT
    pm.id,
    pm.raw_model_name,
    pm.standardized_name,
    pm.outbound_model_name,
    pm.available,
    pm.updated_at
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
WHERE p.code = 'nvidia'
  AND pm.raw_model_name IN (
      'z-ai/glm-5.2',
      'minimaxai/minimax-m3',
      'minimaxai/minimax-m2.7'
  )
ORDER BY pm.raw_model_name, pm.id;

\echo '=== 394 NIM outbound_model_name canonicalization: done ==='
