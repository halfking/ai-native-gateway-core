-- 717 down: restore the pre-alignment request_logs_hot column types.
-- Best-effort by construction: values that cannot re-cast into the legacy
-- types NULL out (documented here, not hidden). hot holds ≤8h of rows, so
-- rollback exposure is bounded.
--
-- Corrections mirroring the fixed up-migration:
-- * customer_id is NOT reverted. 717 never touches it: migration 574 owns
--   text→bigint and ran on every existing install BEFORE 717 existed, so
--   the pre-717 type is bigint. Reverting it to text here would re-break
--   the writer's *int64 binding and the 602 promote projection.
-- * agent_name / agent_type are attempted but expected to warn-and-skip on
--   frozen chains (both are projected by the base wrapper → 0A000), exactly
--   as the up-migration skipped them. They stay varchar — a same-family
--   drift, not a functional break.
-- * Every revert is guarded (only fires when the column is in the 717
--   target type) and wrapped in its own subtransaction so one
--   view-dependency rejection cannot abort the batch.

BEGIN;

DO $$
BEGIN
  -- api_key_fingerprint: varchar(16) → text
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'api_key_fingerprint'
                 AND data_type = 'character varying') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN api_key_fingerprint TYPE text
        USING api_key_fingerprint::text;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: api_key_fingerprint skipped - column is view-dependent (0A000)';
  END;

  -- task_id: varchar(255) → text
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'task_id'
                 AND data_type = 'character varying') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN task_id TYPE text
        USING task_id::text;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: task_id skipped - column is view-dependent (0A000)';
  END;

  -- content_safety_score: jsonb → double precision (non-number payloads NULL)
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'content_safety_score' AND data_type = 'jsonb') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN content_safety_score TYPE double precision
        USING CASE
          WHEN content_safety_score IS NULL THEN NULL
          WHEN jsonb_typeof(content_safety_score) = 'number'
            THEN (content_safety_score::text)::double precision
          ELSE NULL
        END;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: content_safety_score skipped - column is view-dependent (0A000)';
  END;

  -- dlp_violations: jsonb → text[]. USING may not contain subqueries and a
  -- generic jsonb→text[] cast does not exist, so the array collapses to NULL
  -- on rollback (same as the original down revision documented).
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'dlp_violations' AND data_type = 'jsonb') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN dlp_violations TYPE text[] USING NULL;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: dlp_violations skipped - column is view-dependent (0A000)';
  END;

  -- protocol_conversion: boolean → text
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'protocol_conversion' AND data_type = 'boolean') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN protocol_conversion TYPE text
        USING CASE WHEN protocol_conversion IS NULL THEN NULL
                   ELSE protocol_conversion::text END;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: protocol_conversion skipped - column is view-dependent (0A000)';
  END;

  -- ir_extensions / sanitizer_mutations: jsonb → text (serialized JSON)
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'ir_extensions' AND data_type = 'jsonb') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN ir_extensions TYPE text
        USING CASE WHEN ir_extensions IS NULL THEN NULL ELSE ir_extensions::text END;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: ir_extensions skipped - column is view-dependent (0A000)';
  END;

  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'sanitizer_mutations' AND data_type = 'jsonb') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN sanitizer_mutations TYPE text
        USING CASE WHEN sanitizer_mutations IS NULL THEN NULL ELSE sanitizer_mutations::text END;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: sanitizer_mutations skipped - column is view-dependent (0A000)';
  END;

  -- agent_name / agent_type: attempted, expected to warn on frozen chains
  -- (the up-migration could not align them either, so these are usually
  -- no-ops by guard).
  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'agent_name'
                 AND data_type = 'character varying') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN agent_name TYPE text USING agent_name::text;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: agent_name skipped - column is view-dependent (0A000)';
  END;

  BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'request_logs_hot'
                 AND column_name = 'agent_type'
                 AND data_type = 'character varying') THEN
      ALTER TABLE public.request_logs_hot
        ALTER COLUMN agent_type TYPE text USING agent_type::text;
    END IF;
  EXCEPTION WHEN feature_not_supported THEN
    RAISE WARNING '717 down: agent_type skipped - column is view-dependent (0A000)';
  END;

  RAISE NOTICE '717 down: request_logs_hot reverted to pre-alignment types where not view-blocked (customer_id untouched - owned by 574)';
END $$;

COMMIT;
