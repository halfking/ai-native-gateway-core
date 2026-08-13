-- 476: make V2 session uniqueness tenant-scoped.
-- The original 430 constraints omitted tenant_id, allowing client-reused
-- session/request IDs to collide across tenants on the same partition day.

ALTER TABLE public.session_turns
    DROP CONSTRAINT IF EXISTS session_turns_session_id_turn_no_partition_date_key,
    DROP CONSTRAINT IF EXISTS session_turns_request_id_partition_date_key;

ALTER TABLE public.session_turns
    ADD CONSTRAINT session_turns_tenant_session_turn_partition_key
        UNIQUE (tenant_id, session_id, turn_no, partition_date),
    ADD CONSTRAINT session_turns_tenant_request_partition_key
        UNIQUE (tenant_id, request_id, partition_date);

ALTER TABLE public.session_bodies
    DROP CONSTRAINT IF EXISTS session_bodies_session_id_turn_no_partition_date_key,
    DROP CONSTRAINT IF EXISTS session_bodies_request_id_partition_date_key;

ALTER TABLE public.session_bodies
    ADD CONSTRAINT session_bodies_tenant_session_turn_partition_key
        UNIQUE (tenant_id, session_id, turn_no, partition_date),
    ADD CONSTRAINT session_bodies_tenant_request_partition_key
        UNIQUE (tenant_id, request_id, partition_date);

CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_session_turn
    ON public.session_turns (tenant_id, session_id, turn_no DESC);
CREATE INDEX IF NOT EXISTS idx_session_bodies_tenant_session_turn
    ON public.session_bodies (tenant_id, session_id, turn_no DESC);
