-- ============================================================
-- Phase 22 — Roles Sync (matches 184 production)
-- Generated: 2026-06-26
-- Source: 184 \\du output
--
-- Roles on 184 (after restore):
--   kxuser          - Member of {llm_gateway}
--   llm_gateway     - SUPERUSER, CREATEROLE, CREATEDB, REPLICATION, BYPASSRLS
--   casdoor_user    - NOLOGIN role (owns casdoor DB)
--   crm_user        - NOLOGIN role (owns crm DB)
--   doc_tools_user  - NOLOGIN role (owns doc_tools DB)
--   kaixuan_user    - NOLOGIN role (owns kaixuan DB)
-- ============================================================

\connect postgres

-- Create users (idempotent)
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'kxuser') THEN
        CREATE ROLE kxuser;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'llm_gateway') THEN
        CREATE ROLE llm_gateway WITH SUPERUSER CREATEROLE CREATEDB REPLICATION BYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'casdoor_user') THEN
        CREATE ROLE casdoor_user;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'crm_user') THEN
        CREATE ROLE crm_user;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'doc_tools_user') THEN
        CREATE ROLE doc_tools_user;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'kaixuan_user') THEN
        CREATE ROLE kaixuan_user;
    END IF;
END
$$;

-- Add kxuser as member of llm_gateway (matches 184)
GRANT llm_gateway TO kxuser;

-- Set database owners to match 184
-- (Skip stock_trading, port_email, memos, aicms_db which don't have specific owners on 184)
ALTER DATABASE kaixuan OWNER TO llm_gateway;
ALTER DATABASE llm_gateway OWNER TO llm_gateway;
ALTER DATABASE casdoor OWNER TO llm_gateway;
ALTER DATABASE trendaradar OWNER TO llm_gateway;
ALTER DATABASE crm OWNER TO llm_gateway;
ALTER DATABASE brandmind OWNER TO llm_gateway;
ALTER DATABASE brandmind_test OWNER TO llm_gateway;
ALTER DATABASE doc_tools OWNER TO llm_gateway;
ALTER DATABASE geo_flow OWNER TO llm_gateway;
ALTER DATABASE smart_bidding OWNER TO llm_gateway;
ALTER DATABASE stock_trading OWNER TO llm_gateway;
ALTER DATABASE port_email OWNER TO llm_gateway;
ALTER DATABASE memos OWNER TO llm_gateway;
ALTER DATABASE aicms_db OWNER TO llm_gateway;
