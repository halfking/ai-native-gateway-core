-- Migration 863 down (2026-10-14): restore the 714 (tz-pinned, columnar-hard)
-- body of ensure_handoff_logs_partition.
--
-- Rollback semantics: valid only where the columnar access method exists
-- (fresh installs with pgcolumnar). On heap worlds (211 restore lineage) the
-- rollback re-introduces the boot-time P0001 noise and future-month create
-- failures that 863 fixed — roll back deliberately, not routinely.

BEGIN;

\set ON_ERROR_STOP on

CREATE OR REPLACE FUNCTION public.ensure_handoff_logs_partition(p_month timestamptz)
RETURNS void
LANGUAGE plpgsql
AS $function$
DECLARE
    m0 timestamp;
    m1 timestamp;
    part_name text;
    existing_parent oid;
    existing_am text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    m0 := date_trunc('month', p_month AT TIME ZONE 'Asia/Shanghai');
    m1 := m0 + interval '1 month';
    part_name := 'handoff_logs_' || to_char(p_month AT TIME ZONE 'Asia/Shanghai', 'YYYY_MM');

    SELECT i.inhparent, am.amname INTO existing_parent, existing_am
    FROM pg_inherits i
    JOIN pg_class child ON child.oid = i.inhrelid
    JOIN pg_namespace cn ON cn.oid = child.relnamespace
    LEFT JOIN pg_am am ON am.oid = child.relam
    WHERE cn.nspname = 'public' AND child.relname = part_name;

    IF existing_parent IS NOT NULL THEN
        IF existing_parent <> 'public.handoff_logs'::regclass OR existing_am <> 'columnar' THEN
            RAISE EXCEPTION 'public.% exists but is not an attached columnar handoff partition', part_name;
        END IF;
        RETURN;
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.handoff_logs FOR VALUES FROM (%L) TO (%L) USING columnar',
        part_name, m0, m1
    );
    EXECUTE format('CREATE INDEX %I ON public.%I (created_at, id)', part_name || '_created_at_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (session_id, created_at DESC)', part_name || '_session_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (tenant_id, created_at DESC)', part_name || '_tenant_idx', part_name);
    EXECUTE format('CREATE INDEX %I ON public.%I (trigger_reason, created_at DESC)', part_name || '_trigger_idx', part_name);
END;
$function$;

COMMIT;
