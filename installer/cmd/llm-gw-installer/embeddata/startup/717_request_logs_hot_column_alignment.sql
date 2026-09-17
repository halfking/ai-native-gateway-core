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

-- R37 (2026-09-17): every USING expression below reads the column through
-- an explicit ::text cast. USING sees the column's CURRENT type, and after
-- the R36 baseline hand-alignment a fresh install loads request_logs_hot
-- already in the mother types (customer_id bigint, protocol_conversion
-- boolean, *_extensions jsonb) BEFORE this migration runs — bare `col ~
-- regex` / `col IN ('true',...)` / `col = ''` would raise 42883 (operator
-- does not exist: bigint ~ unknown) and abort the fresh-install startup
-- chain. ::text is defined for every source type here, round-trips jsonb
-- losslessly, and keeps drifted (text-typed) installs on the exact legacy
-- expression semantics.
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
            -- {1,18} bounds the match to bigint range: a 19+-digit text
            -- debris value now maps to NULL instead of aborting the whole
            -- migration with a cast overflow.
            WHEN customer_id::text ~ '^[0-9]{1,18}$' THEN customer_id::bigint
            ELSE NULL
        END,
    ALTER COLUMN content_safety_score TYPE jsonb
        USING to_jsonb(content_safety_score),
    ALTER COLUMN dlp_violations TYPE jsonb
        USING to_jsonb(dlp_violations),
    ALTER COLUMN protocol_conversion TYPE boolean
        USING CASE
            WHEN protocol_conversion IS NULL THEN NULL
            WHEN protocol_conversion::text IN ('true', 't', '1', 'yes') THEN TRUE
            ELSE FALSE
        END,
    -- text→jsonb: the gateway writer emits serialized JSON (the $N::text::jsonb
    -- idiom across telemetry/). Regex pre-guard keeps ALTER resilient to
    -- plain-word debris ("pending", "n/a", … → NULL instead of aborting);
    -- PG15-compatible, so it is a start-shape heuristic, not a JSON grammar:
    -- debris *shaped* like JSON ("[garbage") still aborts and must be
    -- cleaned out-of-band (pg_input_is_valid is PG16+). The explicit
    -- true/false/null alternation is required: a bare t/f/n character class
    -- (R36 original) admitted words like "not-json" and then failed the
    -- ::jsonb parse it was guarding.
    ALTER COLUMN ir_extensions TYPE jsonb
        USING CASE
            WHEN ir_extensions IS NULL OR ir_extensions::text = '' THEN NULL
            WHEN ir_extensions::text ~ '^[[:space:]]*([\[\{"-]|-?[0-9]|true|false|null)' THEN ir_extensions::text::jsonb
            ELSE NULL
        END,
    ALTER COLUMN sanitizer_mutations TYPE jsonb
        USING CASE
            WHEN sanitizer_mutations IS NULL OR sanitizer_mutations::text = '' THEN NULL
            WHEN sanitizer_mutations::text ~ '^[[:space:]]*([\[\{"-]|-?[0-9]|true|false|null)' THEN sanitizer_mutations::text::jsonb
            ELSE NULL
        END;
