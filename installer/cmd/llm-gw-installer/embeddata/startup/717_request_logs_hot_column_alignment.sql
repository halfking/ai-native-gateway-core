-- 717 (R36 audit, 2026-09-17): align request_logs_hot column types with the
-- partitioned mother table request_logs. Closes R34 遗留#2.
--
-- Fresh-install blocker: ensureRequestLogsCurrentMonthView (and migration
-- 680:84-107) rebuild the hot∪parent wrapper view from the DYNAMIC
-- hot∩parent column intersection via UNION ALL. With drifted type families
-- on shared columns PostgreSQL raises 42804 and the startup chain aborts
-- (db/db.go calls ensure inside the migration window). Existing installs
-- never rebuild the wrapper (frozen 459-era view), which is why only fresh
-- installs died. Additionally the 602 promote function projects customer_id
-- (hot text → parent bigint, no assignment cast) — every promote batch on a
-- drifted hot table fails 42804, stalling hot drain.
--
-- 603 (2026-08-25) recorded 9 of these as "accepted design divergence"
-- (hot-side simplification, promote uses explicit column lists). That
-- acceptance predates the 680 dynamic-intersection rebuild and is retired
-- by this migration: hot keeps its 8h window (rewrite is seconds, 603:53-61
-- precedent), and the writer bindings are untyped placeholders inferred
-- from the column type (telemetry CustomerID is *int64, ProtocolConversion
-- *bool) — aligning types matches the writer's native types.
--
-- NOTE for reviewers: after this migration the 5 "deliberately omitted"
-- columns in promote_request_logs_hot_to_partition_interval_integer.sql
-- (see its :88-92 comment) could be projected again; revisiting that is a
-- separate change (data-loss question, not a type question).

ALTER TABLE public.request_logs_hot
    -- varchar↔text same-family drift (silent in UNION, noisy in guards):
    ALTER COLUMN agent_name TYPE character varying(255),
    ALTER COLUMN agent_type TYPE character varying(50),
    ALTER COLUMN api_key_fingerprint TYPE character varying(16),
    ALTER COLUMN task_id TYPE character varying(255),
    -- hard type-family drift (42804 in UNION / promote):
    ALTER COLUMN customer_id TYPE bigint
        USING CASE
            WHEN customer_id IS NULL THEN NULL
            WHEN customer_id ~ '^[0-9]+$' THEN customer_id::bigint
            ELSE NULL
        END,
    ALTER COLUMN content_safety_score TYPE jsonb
        USING to_jsonb(content_safety_score),
    ALTER COLUMN dlp_violations TYPE jsonb
        USING to_jsonb(dlp_violations),
    ALTER COLUMN protocol_conversion TYPE boolean
        USING CASE
            WHEN protocol_conversion IS NULL THEN NULL
            WHEN protocol_conversion IN ('true', 't', '1', 'yes') THEN TRUE
            ELSE FALSE
        END,
    -- text→jsonb: the gateway writer emits serialized JSON (the $N::text::jsonb
    -- idiom across telemetry/). Regex pre-guard keeps ALTER resilient to any
    -- out-of-band non-JSON debris (PG15-compatible; no pg_input_is_valid).
    ALTER COLUMN ir_extensions TYPE jsonb
        USING CASE
            WHEN ir_extensions IS NULL OR ir_extensions = '' THEN NULL
            WHEN ir_extensions ~ '^[[:space:]]*[\[\{"0-9tfn-]' THEN ir_extensions::jsonb
            ELSE NULL
        END,
    ALTER COLUMN sanitizer_mutations TYPE jsonb
        USING CASE
            WHEN sanitizer_mutations IS NULL OR sanitizer_mutations = '' THEN NULL
            WHEN sanitizer_mutations ~ '^[[:space:]]*[\[\{"0-9tfn-]' THEN sanitizer_mutations::jsonb
            ELSE NULL
        END;
