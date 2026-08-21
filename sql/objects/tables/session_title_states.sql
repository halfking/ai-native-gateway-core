-- Name: session_title_states; Type: TABLE; Schema: public
-- Durable session-level title state. Redis locks are only an optimization;
-- fencing_token and tombstones are the correctness boundary.
CREATE TABLE public.session_title_states (
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
    CONSTRAINT session_title_states_pkey PRIMARY KEY (tenant_id, scoped_session_id),
    CONSTRAINT session_title_states_token_nonnegative CHECK (fencing_token >= 0),
    CONSTRAINT session_title_states_priority_nonnegative CHECK (source_priority >= 0),
    CONSTRAINT session_title_states_deleted_consistency CHECK (
        (deleted AND deleted_at IS NOT NULL) OR (NOT deleted)
    )
);

COMMENT ON TABLE public.session_title_states IS
    'Durable session-level title state with PostgreSQL fencing and tombstones.';
COMMENT ON COLUMN public.session_title_states.fencing_token IS
    'Monotonically increasing database fencing token; never reuse or delete state.';
COMMENT ON COLUMN public.session_title_states.deleted IS
    'Tombstone set by explicit title DELETE; blocks legacy fallback and background writers.';
