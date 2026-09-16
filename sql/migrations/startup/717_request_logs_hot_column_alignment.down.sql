-- 717 down: restore the pre-alignment request_logs_hot column types.
-- Best-effort by construction: values that cannot re-cast into the legacy
-- types NULL out (documented here, not hidden). hot holds ≤8h of rows, so
-- rollback exposure is bounded.

ALTER TABLE public.request_logs_hot
    ALTER COLUMN agent_name TYPE text,
    ALTER COLUMN agent_type TYPE text,
    ALTER COLUMN api_key_fingerprint TYPE text,
    ALTER COLUMN task_id TYPE text,
    ALTER COLUMN customer_id TYPE text
        USING CASE WHEN customer_id IS NULL THEN NULL ELSE customer_id::text END,
    ALTER COLUMN content_safety_score TYPE double precision
        USING CASE
            WHEN content_safety_score IS NULL THEN NULL
            WHEN jsonb_typeof(content_safety_score) = 'number'
                THEN (content_safety_score::text)::double precision
            ELSE NULL
        END,
    -- jsonb array → text[]: USING may not contain subqueries, so non-array
    -- payloads (and the array itself) collapse to NULL on rollback.
    ALTER COLUMN dlp_violations TYPE text[] USING NULL,
    ALTER COLUMN protocol_conversion TYPE text
        USING CASE WHEN protocol_conversion IS NULL THEN NULL
                   ELSE protocol_conversion::text END,
    ALTER COLUMN ir_extensions TYPE text
        USING CASE WHEN ir_extensions IS NULL THEN NULL ELSE ir_extensions::text END,
    ALTER COLUMN sanitizer_mutations TYPE text
        USING CASE WHEN sanitizer_mutations IS NULL THEN NULL ELSE sanitizer_mutations::text END;
