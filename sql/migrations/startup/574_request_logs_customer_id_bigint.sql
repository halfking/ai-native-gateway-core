-- Migration 574: align request_logs_hot customer_id with the partitioned parent
--
-- Purpose: keep tenant/customer metadata types identical across the hot heap
-- table, partitioned parent, and monthly partitions.
--
-- request_logs.customer_id is BIGINT, while request_logs_hot drifted to TEXT.
-- The hot writer receives *int64 and the mismatch makes the two write paths
-- structurally inconsistent. Existing hot values are numeric or NULL.
--
-- Status: active
-- Idempotent: NO (versioned startup migration)
-- Rollback: 574_request_logs_customer_id_bigint.down.sql
-- Changelog:
--   2026-08-25  v1.0  Align hot customer_id with parent BIGINT type

BEGIN;

ALTER TABLE public.request_logs_hot
    ALTER COLUMN customer_id TYPE bigint
    USING NULLIF(BTRIM(customer_id), '')::bigint;

COMMIT;
