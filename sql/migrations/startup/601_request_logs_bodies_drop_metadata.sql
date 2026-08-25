-- Migration 601: keep body storage independent from request metadata.
--
-- request_logs_bodies_hot is keyed by request_id and stores only body payloads.
-- Tenant/user metadata belongs to request_logs_hot and must be resolved through
-- the request_id join. This migration removes the redundant tenant_id column
-- left by the earlier phase-1 migration.

\set ON_ERROR_STOP on

BEGIN;

ALTER TABLE public.request_logs_bodies_hot
    DROP COLUMN IF EXISTS tenant_id;

COMMIT;
