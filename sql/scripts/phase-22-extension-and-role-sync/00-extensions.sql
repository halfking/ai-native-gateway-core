-- ============================================================
-- Phase 22 — Extensions Sync (matches 184 production)
-- Generated: 2026-06-26
-- Source: 184 (via kubectl exec -n pms-test deploy/llm-gateway-pg)
-- Target: Local dev environment
--
-- Extensions installed on 184 (per database):
--   llm_gateway:  pgcrypto, pg_trgm, btree_gist, citus_columnar
--   casdoor:      (none beyond plpgsql default)
--   kaixuan:      pgcrypto
--   trendaradar:  pg_trgm
--   crm:          pgcrypto, uuid-ossp
--   brandmind:    pgcrypto, uuid-ossp
--   brandmind_test: pgcrypto, uuid-ossp
--   doc_tools:    (none)
--   geo_flow:     (none)
--   smart_bidding: pgcrypto
--   stock_trading: (none)
--   port_email:   (none)
--   memos:        (none)
--   aicms_db:     (none)
-- ============================================================

-- ===========================================================
-- Cluster-level extensions (must be installed in postgres DB)
-- Note: citus is already in shared_preload_libraries (Citus image)
-- ===========================================================
\connect postgres
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
-- citus and citus_columnar come from citusdata/citus image

-- ===========================================================
-- Per-database extensions
-- ===========================================================

\connect llm_gateway
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS citus_columnar;

\connect kaixuan
CREATE EXTENSION IF NOT EXISTS pgcrypto;

\connect trendaradar
CREATE EXTENSION IF NOT EXISTS pg_trgm;

\connect crm
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

\connect brandmind
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

\connect brandmind_test
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

\connect smart_bidding
CREATE EXTENSION IF NOT EXISTS pgcrypto;
