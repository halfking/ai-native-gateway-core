-- =============================================================================
-- 00-prereqs.sql — Required PostgreSQL extensions
-- =============================================================================
-- Run order: FIRST (before 01-schema.sql and 02-seed.sql).
-- Idempotent: every CREATE EXTENSION uses IF NOT EXISTS, safe to re-run.
--
-- Reverse-engineered from production DB (252) on 2026-08-04 via:
--   SELECT extname, extversion FROM pg_extension ORDER BY extname;
--
-- Fix 2026-08-04: citus / citus_columnar must install into pg_catalog
-- (Citus enforces "extension citus must be installed in schema pg_catalog").
-- Production 252 has them in pg_catalog; the original dump dropped the
-- schema info. vector stays in public.
--
-- Regenerate with: ./dump-prereqs.sh (or it runs as part of dump-schema.sh)
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS btree_gist  WITH SCHEMA public;   -- multi-column GiST indexes
CREATE EXTENSION IF NOT EXISTS pg_trgm     WITH SCHEMA public;   -- trigram fuzzy text search
CREATE EXTENSION IF NOT EXISTS pgcrypto    WITH SCHEMA public;   -- gen_random_uuid(), crypt(), etc.
CREATE EXTENSION IF NOT EXISTS plpgsql     WITH SCHEMA pg_catalog;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public; -- query stats
CREATE EXTENSION IF NOT EXISTS pgstattuple WITH SCHEMA public;   -- table/blob stats
CREATE EXTENSION IF NOT EXISTS citus       WITH SCHEMA pg_catalog; -- distributed/columnar tables
CREATE EXTENSION IF NOT EXISTS citus_columnar WITH SCHEMA pg_catalog; -- columnar access method
CREATE EXTENSION IF NOT EXISTS vector      WITH SCHEMA public;   -- pgvector embeddings

-- Verify installed extensions match production
-- Expected:
--   btree_gist 1.7
--   citus 13.3-1
--   citus_columnar 13.3-1
--   pg_stat_statements 1.11
--   pg_trgm 1.6
--   pgcrypto 1.3
--   pgstattuple 1.5
--   plpgsql 1.0
--   vector 0.8.3
