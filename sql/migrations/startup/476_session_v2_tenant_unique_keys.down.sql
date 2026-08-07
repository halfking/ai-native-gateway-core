-- 476: revert tenant-scoped V2 uniqueness constraints.

ALTER TABLE gateway.session_turns
    DROP CONSTRAINT IF EXISTS session_turns_tenant_session_turn_partition_key,
    DROP CONSTRAINT IF EXISTS session_turns_tenant_request_partition_key;

ALTER TABLE gateway.session_turns
    ADD CONSTRAINT session_turns_session_id_turn_no_partition_date_key
        UNIQUE (session_id, turn_no, partition_date),
    ADD CONSTRAINT session_turns_request_id_partition_date_key
        UNIQUE (request_id, partition_date);

ALTER TABLE gateway.session_bodies
    DROP CONSTRAINT IF EXISTS session_bodies_tenant_session_turn_partition_key,
    DROP CONSTRAINT IF EXISTS session_bodies_tenant_request_partition_key;

ALTER TABLE gateway.session_bodies
    ADD CONSTRAINT session_bodies_session_id_turn_no_partition_date_key
        UNIQUE (session_id, turn_no, partition_date),
    ADD CONSTRAINT session_bodies_request_id_partition_date_key
        UNIQUE (request_id, partition_date);

DROP INDEX IF EXISTS gateway.idx_session_turns_tenant_session_turn;
DROP INDEX IF EXISTS gateway.idx_session_bodies_tenant_session_turn;
