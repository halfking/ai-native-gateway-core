--
-- Name: model_probe_reclaim_idle_slots(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_reclaim_idle_slots(reclaim_after_seconds integer) RETURNS TABLE(deleted_slots integer, deleted_pins integer)
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_deleted_slots INTEGER := 0;
    v_deleted_pins  INTEGER := 0;
    v_cutoff        TIMESTAMPTZ := NOW() - make_interval(secs => reclaim_after_seconds);
    rec             RECORD;
BEGIN
    -- Iterate over currently-occupied slots whose holder has been idle
    -- (no recent traffic on the holder identity) for longer than the
    -- cutoff. We use Redis-side expiration timestamps via the slot key
    -- TTL as the activity signal: a slot's TTL is refreshed on every
    -- Release(). If the TTL is below the cutoff, the holder has been
    -- idle since the last refresh.
    --
    -- We don't have direct access to Redis from plpgsql, so this SQL
    -- function targets the model_probe_state table (which mirrors the
    -- Redis slot via the runner's recordRun writes).
    --
    -- The Go goroutine in credentialfpslot handles the actual Redis
    -- DEL via the same Lua script used by ResetSlots. This SQL function
    -- is a companion for ops tooling and consistency checks.
    FOR rec IN
        SELECT credential_id, raw_model_name
        FROM model_probe_state
        WHERE last_attempt_at < v_cutoff
          AND state <> 'broken_confirmed'
    LOOP
        UPDATE model_probe_state
        SET state = 'unknown',
            consecutive_successes = 0,
            consecutive_failures = 0,
            next_retry_at = NOW() + INTERVAL '2 hours',
            -- do NOT change last_attempt_at — we want it to remain the
            -- "last activity" anchor for future audit queries.
            last_state_change_at = NOW()
        WHERE credential_id = rec.credential_id
          AND raw_model_name = rec.raw_model_name;
        v_deleted_slots := v_deleted_slots + 1;
    END LOOP;

    RETURN QUERY SELECT v_deleted_slots, v_deleted_pins;
END;
$$;

