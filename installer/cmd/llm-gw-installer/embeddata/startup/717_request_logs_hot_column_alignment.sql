-- 717 (R36 audit, 2026-09-17; corrected same day after live-DB audit): align
-- request_logs_hot column types with the partitioned mother table
-- request_logs. Closes R34 遗留#2.
--
-- == Why the first two revisions were withdrawn ==
-- * Rev 1 wrote `customer_id ~ '^[0-9]+$'`. Wherever customer_id is bigint
--   that is a hard parse error (42883 "operator does not exist: bigint ~
--   unknown" — pg_cast carries no bigint/integer→text edge at all, confirmed
--   live). And customer_id is bigint EVERYWHERE: migration 574 (2026-08-25)
--   already converted hot.customer_id text→bigint on every existing install,
--   and the installer baseline (01-schema.sql:~13417 block) creates it
--   bigint from birth. An intermediate same-day patch switched the operand
--   to customer_id::text, which fixes the parse but not the statement:
-- * Rev 2 still failed everywhere because the whole batch is ONE ALTER TABLE
--   statement, and PostgreSQL rejects `ALTER COLUMN ... TYPE` on ANY column
--   a view depends on (transitively via pg_rewrite) with 0A000 "cannot alter
--   type of a column used by a view or rule" — BEFORE comparing old/new
--   types, so even a type-identical no-op dies. On the frozen 459-era
--   wrapper chain carried by local/preprod/prod, three of the nine columns
--   are blocked: agent_name + agent_type (projected by the base wrapper
--   request_logs_with_current_month_without_customer_id) and customer_id
--   (referenced by the class-wrapper customer_id lateral). Fresh installs
--   are equally blocked: the installer replays 680 before 717, so the chain
--   already exists when this file runs.
--
-- == Scope re-derived from live facts ==
-- * customer_id: clause withdrawn entirely. 574 owns the text→bigint
--   transition and is applied on every existing install; the installer
--   baseline is born aligned. The original revision's premise ("hot text →
--   parent bigint", citing 602) predates 574 and was stale.
-- * Fresh installs need NOTHING here: the installer baseline already creates
--   all nine columns aligned (varchar(255/50/16/255) / bigint / jsonb /
--   boolean). The type guard below turns this file into a no-op there —
--   which also retires the original "fresh-install 42804 blocker" framing:
--   the current baseline cannot drift in the first place.
-- * The remaining eight drifts are handled per column, each in its own
--   subtransaction:
--     - On the frozen chain (local/preprod/prod norm) seven columns are
--       view-free and alter in place: api_key_fingerprint, task_id,
--       content_safety_score, dlp_violations, protocol_conversion,
--       ir_extensions, sanitizer_mutations. These carry the real damage
--       (42804 in any future dynamic-intersection view rebuild; the
--       boolean/jsonb columns fight the writer's native binding types).
--     - agent_name / agent_type stay view-dependent → 0A000 → WARNING +
--       skip. text↔varchar is the same type family (binary coercible,
--       silent in UNION), so the residual harm is guard noise only. Align
--       them during the next planned view-chain rebuild (692 drop→alter→
--       recreate pattern), or on any future vintage whose chain no longer
--       projects them (re-running this idempotent file picks them up).
--     - On databases whose chain was dynamically rebuilt post-680, the base
--       wrapper projects more shared columns, so additional columns may warn
--       and skip the same way. The batch never aborts: worst case is a fully
--       warned no-op, best case (the norm) is a seven-column alignment.
--
-- 603 (2026-08-25) recorded these divergences as "accepted design
-- divergence". That acceptance is retired for the columns altered here:
-- hot keeps its ≤8h window (rewrite is seconds, 603:53-61 precedent), and
-- the writer bindings are untyped placeholders inferred from the column
-- type (telemetry CustomerID is *int64, ProtocolConversion *bool) —
-- aligning types matches the writer's native types.
--
-- NOTE for reviewers: after this migration the 5 "deliberately omitted"
-- columns in promote_request_logs_hot_to_partition_interval_integer.sql
-- (see its :88-92 comment) could be projected again; revisiting that is a
-- separate change (data-loss question, not a type question).

BEGIN;

DO $$
DECLARE
  v_remaining text;
BEGIN
  -- Per-column guard + alter. Each column runs in its own subtransaction so
  -- one view-dependency rejection (0A000) cannot abort its siblings; the
  -- migration stays idempotent (already-aligned columns skip silently).
  --
  -- NULL-safe LEFT() bounds varchar retypes to the parent column's own
  -- domain, so a >length legacy value can neither fail the rewrite here nor
  -- a later promote (parent varchar(n) would 22001 on it anyway).

  -- api_key_fingerprint: text → varchar(16)
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'api_key_fingerprint' AND data_type = 'text') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN api_key_fingerprint TYPE character varying(16)
        USING LEFT(api_key_fingerprint, 16);
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: api_key_fingerprint skipped - column is view-dependent (0A000)';
  END;

  -- task_id: text → varchar(255)
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'task_id' AND data_type = 'text') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN task_id TYPE character varying(255)
        USING LEFT(task_id, 255);
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: task_id skipped - column is view-dependent (0A000)';
  END;

  -- content_safety_score: double precision → jsonb
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'content_safety_score'
                 AND data_type = 'double precision') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN content_safety_score TYPE jsonb
        USING to_jsonb(content_safety_score);
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: content_safety_score skipped - column is view-dependent (0A000)';
  END;

  -- dlp_violations: text[] → jsonb (JSON array of the legacy strings)
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'dlp_violations' AND data_type = 'ARRAY') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN dlp_violations TYPE jsonb
        USING to_jsonb(dlp_violations);
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: dlp_violations skipped - column is view-dependent (0A000)';
  END;

  -- protocol_conversion: text → boolean (writer binds *bool)
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'protocol_conversion' AND data_type = 'text') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN protocol_conversion TYPE boolean
        USING CASE
          WHEN protocol_conversion IS NULL THEN NULL
          WHEN protocol_conversion IN ('true', 't', '1', 'yes') THEN TRUE
          ELSE FALSE
        END;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: protocol_conversion skipped - column is view-dependent (0A000)';
  END;

  -- ir_extensions / sanitizer_mutations: text → jsonb. The gateway writer
  -- emits serialized JSON (the $N::text::jsonb idiom across telemetry/). The
  -- regex pre-guard keeps ALTER resilient to any out-of-band non-JSON debris
  -- (PG15-compatible; no pg_input_is_valid).
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'ir_extensions' AND data_type = 'text') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN ir_extensions TYPE jsonb
        USING CASE
          WHEN ir_extensions IS NULL OR ir_extensions = '' THEN NULL
          WHEN ir_extensions ~ '^[[:space:]]*[\[\{"0-9tfn-]' THEN ir_extensions::jsonb
          ELSE NULL
        END;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: ir_extensions skipped - column is view-dependent (0A000)';
  END;

  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'sanitizer_mutations' AND data_type = 'text') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN sanitizer_mutations TYPE jsonb
        USING CASE
          WHEN sanitizer_mutations IS NULL OR sanitizer_mutations = '' THEN NULL
          WHEN sanitizer_mutations ~ '^[[:space:]]*[\[\{"0-9tfn-]' THEN sanitizer_mutations::jsonb
          ELSE NULL
        END;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: sanitizer_mutations skipped - column is view-dependent (0A000)';
  END;

  -- agent_name / agent_type: attempted last and EXPECTED to warn on frozen
  -- chains (the base wrapper projects both). Kept here so a future vintage
  -- whose chain no longer references them self-aligns on re-run.
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'agent_name' AND data_type = 'text') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN agent_name TYPE character varying(255)
        USING LEFT(agent_name, 255);
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: agent_name skipped - view-dependent on the wrapper chain; align during the next planned view-chain rebuild (692 pattern)';
  END;

  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'agent_type' AND data_type = 'text') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN agent_type TYPE character varying(50)
        USING LEFT(agent_type, 50);
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717: agent_type skipped - view-dependent on the wrapper chain; align during the next planned view-chain rebuild (692 pattern)';
  END;

  -- Post-batch report: anything still off-target stays visible in the log.
  SELECT string_agg(column_name || ':' || data_type, ', ' ORDER BY column_name)
    INTO v_remaining
    FROM information_schema.columns
   WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
     AND column_name IN ('agent_name', 'agent_type', 'api_key_fingerprint',
                         'task_id', 'content_safety_score', 'dlp_violations',
                         'protocol_conversion', 'ir_extensions', 'sanitizer_mutations')
     AND (column_name, data_type) NOT IN (
       ('agent_name', 'character varying'),
       ('agent_type', 'character varying'),
       ('api_key_fingerprint', 'character varying'),
       ('task_id', 'character varying'),
       ('content_safety_score', 'jsonb'),
       ('dlp_violations', 'jsonb'),
       ('protocol_conversion', 'boolean'),
       ('ir_extensions', 'jsonb'),
       ('sanitizer_mutations', 'jsonb'));
  IF v_remaining IS NOT NULL THEN
    RAISE NOTICE '717: columns still off-target after alignment: %', v_remaining;
  ELSE
    RAISE NOTICE '717: request_logs_hot column alignment on target';
  END IF;
END $$;

COMMIT;
