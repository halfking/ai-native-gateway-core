-- =============================================================================
-- 00-prereqs.sql — Required PostgreSQL extensions
-- =============================================================================
-- Run order: FIRST (before 01-schema.sql and 02-seed.sql).
-- Idempotent: every CREATE EXTENSION uses IF NOT EXISTS, safe to re-run.
--
-- Reverse-engineered from production DB on 2026-06-24 via:
--   SELECT extname, extversion FROM pg_extension WHERE extname IN (...);
--
-- Regenerate with: ./dump-prereqs.sh (or it runs as part of dump-schema.sh)
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS btree_gist  WITH SCHEMA public;   -- multi-column GiST indexes
CREATE EXTENSION IF NOT EXISTS pg_trgm     WITH SCHEMA public;   -- trigram fuzzy text search
CREATE EXTENSION IF NOT EXISTS pgcrypto    WITH SCHEMA public;   -- gen_random_uuid(), crypt(), etc.
CREATE EXTENSION IF NOT EXISTS plpgsql     WITH SCHEMA pg_catalog;

-- Verify installed extensions match production
-- Expected:
--   btree_gist 1.7
--   pg_trgm 1.6
--   pgcrypto 1.3
--   plpgsql 1.0
