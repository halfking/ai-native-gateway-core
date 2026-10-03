-- Migration 550: durable session-level title state.
-- Additive and idempotent. Do not drop this table during rollback: old binaries
-- may continue writing legacy projections, but must not erase fencing state.

BEGIN;

CREATE TABLE IF NOT EXISTS public.session_title_states (
    tenant_id         text NOT NULL,
    scoped_session_id text NOT NULL,
    title             text,
    deleted           boolean NOT NULL DEFAULT false,
    deleted_at        timestamptz,
    fencing_token     bigint NOT NULL DEFAULT 0,
    lease_owner       text,
    lease_expires_at  timestamptz,
    source            text NOT NULL DEFAULT 'unknown',
    source_priority   integer NOT NULL DEFAULT 0,
    source_task_id    text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, scoped_session_id)
);

ALTER TABLE public.session_title_states
    ADD COLUMN IF NOT EXISTS title text,
    ADD COLUMN IF NOT EXISTS deleted boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS deleted_at timestamptz,
    ADD COLUMN IF NOT EXISTS fencing_token bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_owner text,
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS source_priority integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS source_task_id text,
    ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

COMMENT ON TABLE public.session_title_states IS
    'Durable session-level title state with PostgreSQL fencing and tombstones.';
COMMENT ON COLUMN public.session_title_states.fencing_token IS
    'Monotonically increasing database fencing token; never reuse or delete state.';
COMMENT ON COLUMN public.session_title_states.deleted IS
    'Tombstone set by explicit title DELETE; blocks legacy fallback and background writers.';

COMMIT;
