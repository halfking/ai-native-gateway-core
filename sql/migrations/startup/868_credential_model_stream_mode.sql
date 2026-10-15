-- 868_credential_model_stream_mode.sql
-- Bind stream transport policy to one credential-model pair.
-- Existing rows default to both modes; Vapeur Claude bindings are restricted
-- to non-stream because that upstream/model combination rejects streaming.

BEGIN;

ALTER TABLE public.credential_model_bindings
    ADD COLUMN IF NOT EXISTS stream_mode text NOT NULL DEFAULT 'both';

ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS credential_model_bindings_stream_mode_check;

ALTER TABLE public.credential_model_bindings
    ADD CONSTRAINT credential_model_bindings_stream_mode_check
    CHECK (stream_mode IN ('both', 'stream_only', 'non_stream_only'));

UPDATE public.credential_model_bindings cmb
SET stream_mode = 'non_stream_only',
    updated_at = NOW()
FROM public.credentials c
JOIN public.providers p ON p.id = c.provider_id
JOIN public.provider_models pm ON pm.provider_id = p.id
WHERE cmb.credential_id = c.id
  AND cmb.provider_model_id = pm.id
  AND lower(p.code) = 'vapeur'
  AND (
      lower(pm.raw_model_name) LIKE 'claude%'
      OR lower(COALESCE(pm.standardized_name, '')) LIKE 'claude%'
      OR lower(COALESCE(pm.outbound_model_name, '')) LIKE 'claude%'
  )
  AND cmb.stream_mode IS DISTINCT FROM 'non_stream_only';

COMMENT ON COLUMN public.credential_model_bindings.stream_mode IS
    'Allowed client transport for this credential-model binding: both (default), stream_only, or non_stream_only.';

COMMIT;
