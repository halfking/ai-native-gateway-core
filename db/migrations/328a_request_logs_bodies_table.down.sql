-- Migration 328a down: drop request_logs_bodies (only safe if migration 328b
-- has not yet dropped the body columns on request_logs; otherwise backfill
-- data is lost).
--
-- Author: llm-gateway-ops (2026-07-02)

DROP FUNCTION IF EXISTS ensure_request_logs_bodies_partition(timestamptz);
DROP TABLE IF EXISTS public.request_logs_bodies CASCADE;
