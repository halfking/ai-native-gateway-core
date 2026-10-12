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
CREATE EXTENSION IF NOT EXISTS vector      WITH SCHEMA public;   -- pgvector embeddings

-- Columnar storage: Citus is the production choice, but it is not available on
-- every host (it needs a C toolchain and a coordinated build). The MIT
-- pgColumnar extension provides the same table access method semantics under a
-- different name (`pgcolumnar` instead of `columnar`), so fall back to it
-- rather than aborting the whole schema on a host without Citus.
--
-- The rest of this schema (and 01-schema.sql) spells the access method
-- `USING columnar`. Register that name as an alias of whichever implementation
-- is present, so the DDL below is identical on Citus and pgColumnar hosts.
DO $$
DECLARE
  have_citus boolean;
BEGIN
  BEGIN
    CREATE EXTENSION IF NOT EXISTS citus WITH SCHEMA pg_catalog;
    have_citus := true;
  EXCEPTION WHEN OTHERS THEN
    have_citus := false;
  END;

  IF have_citus THEN
    BEGIN
      CREATE EXTENSION IF NOT EXISTS citus_columnar WITH SCHEMA pg_catalog;
    EXCEPTION WHEN OTHERS THEN
      RAISE NOTICE 'citus present but citus_columnar unavailable: %', SQLERRM;
    END;
  ELSE
    RAISE NOTICE 'citus not available; falling back to pgcolumnar for USING columnar';
    BEGIN
      CREATE EXTENSION IF NOT EXISTS pgcolumnar;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'neither citus nor pgcolumnar is installable: %', SQLERRM;
    END;
  END IF;

  -- Expose the canonical `columnar` access-method name if only pgColumnar
  -- supplied it. CREATE ACCESS METHOD has no IF NOT EXISTS, so check first.
  -- pgColumnar registers its handler as pgcolumnar.columnar_handler; Citus
  -- calls its own citus_columnar access method `columnar` directly, so this
  -- branch only runs on the pgColumnar fallback path.
  IF NOT EXISTS (SELECT 1 FROM pg_am WHERE amname = 'columnar') THEN
    EXECUTE 'CREATE ACCESS METHOD columnar TYPE TABLE HANDLER pgcolumnar.columnar_handler';
  END IF;
END
$$;

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
