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
--
-- R37 (2026-09-17) 加固（本机真库首跑实录，原体从未在存量库执行过）：
-- ① USING 表达式原先无条件假定漂移形态（customer_id text 上跑 `~` 正则、
--   protocol_conversion text 上跑 IN ('true',...)）——在个别列已被先前修复
--   对齐的存量库上（本机实测 customer_id 已是 bigint）42883 直接中止序列。
--   改为按 information_schema 逐列守卫：仅漂移列执行 ALTER，已对齐列跳过。
-- ② request_logs_hot 的列被 4 个静态视图依赖（request_logs_with_current_month
--   及其两个中间 wrapper、routing_analytics_source），ALTER TYPE 会因
--   "cannot alter type of a column used by a view" 42883 中止。改为单 DO 块：
--   先 pg_get_viewdef 捕获现体 → DROP → ALTER → 按捕获体重建（对齐后类型
--   兼容性只增不减，冻结体依然合法）。整块为单语句，psql autocommit 下原子；
--   缺视图的世系（全新安装早期）各步均守卫跳过，ensure 链会在启动时补建。

DO $$
DECLARE
  v_type  text;
  v_final text;
  v_class text;
  v_base  text;
  v_src   text;
BEGIN
  -- ── 1. 捕获依赖视图现体（存在才捕获）─────────────────────────────────
  IF to_regclass('public.request_logs_with_current_month') IS NOT NULL THEN
    v_final := pg_get_viewdef('public.request_logs_with_current_month'::regclass, true);
  END IF;
  IF to_regclass('public.request_logs_with_current_month_without_request_class_due_at') IS NOT NULL THEN
    v_class := pg_get_viewdef('public.request_logs_with_current_month_without_request_class_due_at'::regclass, true);
  END IF;
  IF to_regclass('public.request_logs_with_current_month_without_customer_id') IS NOT NULL THEN
    v_base := pg_get_viewdef('public.request_logs_with_current_month_without_customer_id'::regclass, true);
  END IF;
  IF to_regclass('public.routing_analytics_source') IS NOT NULL THEN
    v_src := pg_get_viewdef('public.routing_analytics_source'::regclass, true);
  END IF;

  -- ── 2. 自顶向下 drop（CASCADE 兜底传递依赖）──────────────────────────
  DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE;
  DROP VIEW IF EXISTS public.request_logs_with_current_month_without_request_class_due_at CASCADE;
  DROP VIEW IF EXISTS public.request_logs_with_current_month_without_customer_id CASCADE;
  DROP VIEW IF EXISTS public.routing_analytics_source CASCADE;

  -- ── 3. 守卫式列型对齐（仅漂移列）─────────────────────────────────────
  -- varchar↔text same-family drift (silent in UNION, noisy in guards):
  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='agent_name';
  IF v_type IS NOT NULL AND v_type <> 'character varying' THEN
    EXECUTE 'ALTER TABLE public.request_logs_hot ALTER COLUMN agent_name TYPE character varying(255)';
  END IF;

  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='agent_type';
  IF v_type IS NOT NULL AND v_type <> 'character varying' THEN
    EXECUTE 'ALTER TABLE public.request_logs_hot ALTER COLUMN agent_type TYPE character varying(50)';
  END IF;

  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='api_key_fingerprint';
  IF v_type IS NOT NULL AND v_type <> 'character varying' THEN
    EXECUTE 'ALTER TABLE public.request_logs_hot ALTER COLUMN api_key_fingerprint TYPE character varying(16)';
  END IF;

  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='task_id';
  IF v_type IS NOT NULL AND v_type <> 'character varying' THEN
    EXECUTE 'ALTER TABLE public.request_logs_hot ALTER COLUMN task_id TYPE character varying(255)';
  END IF;

  -- hard type-family drift (42804 in UNION / promote):
  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='customer_id';
  IF v_type = 'text' OR v_type = 'character varying' OR v_type = 'character' THEN
    EXECUTE $alt$ALTER TABLE public.request_logs_hot ALTER COLUMN customer_id TYPE bigint
        USING CASE
            WHEN customer_id IS NULL THEN NULL
            WHEN customer_id::text ~ '^[0-9]+$' THEN customer_id::text::bigint
            ELSE NULL
        END$alt$;
  END IF;

  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='content_safety_score';
  IF v_type IS NOT NULL AND v_type <> 'jsonb' THEN
    EXECUTE 'ALTER TABLE public.request_logs_hot ALTER COLUMN content_safety_score TYPE jsonb USING to_jsonb(content_safety_score)';
  END IF;

  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='dlp_violations';
  IF v_type IS NOT NULL AND v_type <> 'jsonb' THEN
    EXECUTE 'ALTER TABLE public.request_logs_hot ALTER COLUMN dlp_violations TYPE jsonb USING to_jsonb(dlp_violations)';
  END IF;

  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='protocol_conversion';
  IF v_type = 'text' OR v_type = 'character varying' OR v_type = 'character' THEN
    EXECUTE $alt$ALTER TABLE public.request_logs_hot ALTER COLUMN protocol_conversion TYPE boolean
        USING CASE
            WHEN protocol_conversion IS NULL THEN NULL
            WHEN protocol_conversion::text IN ('true', 't', '1', 'yes') THEN TRUE
            ELSE FALSE
        END$alt$;
  END IF;

  -- text→jsonb: the gateway writer emits serialized JSON (the $N::text::jsonb
  -- idiom across telemetry/). Regex pre-guard keeps ALTER resilient to any
  -- out-of-band non-JSON debris (PG15-compatible; no pg_input_is_valid).
  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='ir_extensions';
  IF v_type = 'text' OR v_type = 'character varying' OR v_type = 'character' THEN
    EXECUTE $alt$ALTER TABLE public.request_logs_hot ALTER COLUMN ir_extensions TYPE jsonb
        USING CASE
            WHEN ir_extensions IS NULL OR ir_extensions = '' THEN NULL
            WHEN ir_extensions ~ '^[[:space:]]*[\[\{"0-9tfn-]' THEN ir_extensions::jsonb
            ELSE NULL
        END$alt$;
  END IF;

  SELECT data_type INTO v_type FROM information_schema.columns
   WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='sanitizer_mutations';
  IF v_type = 'text' OR v_type = 'character varying' OR v_type = 'character' THEN
    EXECUTE $alt$ALTER TABLE public.request_logs_hot ALTER COLUMN sanitizer_mutations TYPE jsonb
        USING CASE
            WHEN sanitizer_mutations IS NULL OR sanitizer_mutations = '' THEN NULL
            WHEN sanitizer_mutations ~ '^[[:space:]]*[\[\{"0-9tfn-]' THEN sanitizer_mutations::jsonb
            ELSE NULL
        END$alt$;
  END IF;

  -- ── 4. 按捕获体重建依赖视图（缺省世系跳过，启动 ensure 兜底）─────────
  IF v_base IS NOT NULL THEN
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_customer_id AS ' || v_base;
  END IF;
  IF v_class IS NOT NULL THEN
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS ' || v_class;
  END IF;
  IF v_final IS NOT NULL THEN
    EXECUTE 'CREATE VIEW public.request_logs_with_current_month AS ' || v_final;
  END IF;
  IF v_src IS NOT NULL THEN
    EXECUTE 'CREATE VIEW public.routing_analytics_source AS ' || v_src;
  END IF;
END
$$;
