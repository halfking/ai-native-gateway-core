-- provider_events table (already on 252; ensure local parity for provider profile alerts)
BEGIN;
CREATE TABLE IF NOT EXISTS provider_events (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    event_kind text NOT NULL,
    payload_json jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS provider_events_id_seq
    AS bigint START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE provider_events_id_seq OWNED BY provider_events.id;
ALTER TABLE provider_events ALTER COLUMN id SET DEFAULT nextval('provider_events_id_seq');
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='provider_events_pkey' AND conrelid='provider_events'::regclass) THEN
        ALTER TABLE provider_events ADD CONSTRAINT provider_events_pkey PRIMARY KEY (id);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_provider_events_credential_ts ON provider_events (credential_id, ts DESC);
COMMIT;
