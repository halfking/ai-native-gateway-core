-- Migration 620: make provider-error aggregation resumable and idempotent.
-- A dedicated monotonic source key is used because candidate_failure_logs_hot.id
-- is a legacy, non-unique business field and cannot safely be a watermark.

BEGIN;

CREATE SEQUENCE IF NOT EXISTS public.candidate_failure_logs_hot_aggregation_id_seq;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    ADD COLUMN IF NOT EXISTS aggregation_id bigint;

UPDATE public.candidate_failure_logs_hot
SET aggregation_id = nextval('public.candidate_failure_logs_hot_aggregation_id_seq')
WHERE aggregation_id IS NULL;

DO $$
DECLARE
    max_id bigint;
BEGIN
    SELECT MAX(aggregation_id) INTO max_id FROM public.candidate_failure_logs_hot;
    IF max_id IS NULL OR max_id < 1 THEN
        PERFORM setval('public.candidate_failure_logs_hot_aggregation_id_seq', 1, false);
    ELSE
        PERFORM setval('public.candidate_failure_logs_hot_aggregation_id_seq', max_id, true);
    END IF;
END
$$;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    ALTER COLUMN aggregation_id SET DEFAULT nextval('public.candidate_failure_logs_hot_aggregation_id_seq');

ALTER SEQUENCE public.candidate_failure_logs_hot_aggregation_id_seq
    OWNED BY public.candidate_failure_logs_hot.aggregation_id;

CREATE TABLE IF NOT EXISTS public.provider_error_aggregator_state (
    id integer PRIMARY KEY,
    last_source_id bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_error_aggregator_state_singleton CHECK (id = 1)
);

INSERT INTO public.provider_error_aggregator_state (id, last_source_id)
VALUES (1, 0)
ON CONFLICT (id) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_cfl_hot_aggregation_id
    ON public.candidate_failure_logs_hot (aggregation_id);

COMMIT;
