-- Rollback migration 694: restore pre-694 ensure function definitions.

BEGIN;

CREATE OR REPLACE FUNCTION public.ensure_candidate_failure_logs_partition(target_ts timestamp with time zone) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'candidate_failure_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 689: heap (was columnar). Row-level DELETE (the 7d TTL trim path
        -- in bg/opslog_trimmer.go) and the hot→monthly promote chain both
        -- need UPDATE/DELETE-capable storage; columnar partitions are
        -- append-only (established by migration 562).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF candidate_failure_logs
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_candidate_failure_logs_partition: created % as heap', partition_name;
    END IF;
    -- 689: dropped the former ELSE-branch enforce_columnar_partition() call —
    -- partitions are heap now and must stay heap.
    RETURN partition_name;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_credential_model_index_partition(target_month timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start date := date_trunc('month', target_month)::date;
    month_end   date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'credential_model_index_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF credential_model_index
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'Created partition % for credential_model_index', partition_name;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 2026-08-23 (migration 562): switched from columnar to heap. The
        -- body columns (request_body / outbound_body / response_body jsonb)
        -- are TOAST-heavy (~350 KB avg) and the hot→monthly promote path
        -- issues INSERT-then-DELETE-when-retried cycles; columnar blocks
        -- UPDATE/DELETE so the bodies pipeline silently stalled, leaving
        -- request_logs_bodies_hot unbounded. request_logs_archive remains
        -- columnar (it's a read-only tiered store, see archive_request_logs).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date := date_trunc('month', target_ts)::date;
    month_end     date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name     text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-06-24 (migration 043): GIN trgm on client_model so the
        -- /api/logs ?model= ILIKE filter can use a bitmap index scan
        -- instead of a partition Seq Scan once volume grows.
        EXECUTE format(
            'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_request_wal_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$ DECLARE month_start date := date_trunc('month', target_ts)::date; month_end date := (date_trunc('month', target_ts) + interval '1 month')::date; part_name text := 'request_wal_' || to_char(month_start, 'YYYY_MM'); BEGIN IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name AND relnamespace = 'public'::regnamespace) THEN EXECUTE format('CREATE TABLE %I PARTITION OF request_wal FOR VALUES FROM (%L) TO (%L)', part_name, month_start, month_end); END IF; END; $$;

CREATE OR REPLACE FUNCTION public.ensure_routing_decision_log_partition(target_month timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start date := date_trunc('month', target_month)::date;
    month_end   date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'routing_decision_log_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF routing_decision_log
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'Created partition % for routing_decision_log', partition_name;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_usage_ledger_partition(target_month timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date := date_trunc('month', target_month)::date;
    month_end      date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'usage_ledger_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF usage_ledger
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_usage_ledger_partition: created % as heap', partition_name;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_next_month_archive_partition() RETURNS void
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
		    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
		    partition_name   text := 'request_logs_archive_' || to_char(next_month_start, 'YYYY_MM');
		BEGIN
		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
		        EXECUTE format(
		            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
		            partition_name, next_month_start, next_month_end
		        );
		    END IF;
		END;
		$$;

CREATE OR REPLACE FUNCTION public.ensure_next_month_cmi_archive_partition() RETURNS void
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
		    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
		    partition_name   text := 'credential_model_index_archive_' || to_char(next_month_start, 'YYYY_MM');
		BEGIN
		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
		        EXECUTE format(
		            'CREATE TABLE %I PARTITION OF credential_model_index_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
		            partition_name, next_month_start, next_month_end
		        );
		    END IF;
		END;
		$$;

CREATE OR REPLACE FUNCTION public.ensure_next_month_request_wal_partition() RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
    partition_name   text := 'request_wal_' || to_char(next_month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
        -- 2026-08-23 (migration 562): switched from columnar to heap.
        -- request_wal is a heap parent; columnar partitions blocked the
        -- hot→monthly promote path. Kept in sync with
        -- ensure_request_wal_partition(timestamptz) (the active call
        -- site). This orphan is retained for backwards compatibility
        -- but now matches the active function's storage policy.
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_wal FOR VALUES FROM (%L) TO (%L)',
            partition_name, next_month_start, next_month_end
        );
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_next_month_routing_archive_partition() RETURNS void
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
		    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
		    partition_name   text := 'routing_decision_log_archive_' || to_char(next_month_start, 'YYYY_MM');
		BEGIN
		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
		        EXECUTE format(
		            'CREATE TABLE %I PARTITION OF routing_decision_log_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
		            partition_name, next_month_start, next_month_end
		        );
		    END IF;
		END;
		$$;

CREATE OR REPLACE FUNCTION public.ensure_tool_usage_stats_partition(
    target_month timestamp with time zone DEFAULT now()
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    partition_name text;
    start_date date;
    end_date   date;
BEGIN
    start_date := date_trunc('month', target_month)::date;
    end_date   := (start_date + interval '1 month')::date;
    partition_name := 'tool_usage_stats_' || to_char(start_date, 'YYYY_MM');

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.tool_usage_stats FOR VALUES FROM (%L) TO (%L)',
        partition_name, start_date, end_date
    );

    RAISE NOTICE 'ensure_tool_usage_stats_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

CREATE OR REPLACE FUNCTION public.ensure_credit_ledger_partition(
    target_month timestamp with time zone DEFAULT now()
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    partition_name text;
    start_date timestamp with time zone;
    end_date   timestamp with time zone;
BEGIN
    start_date := date_trunc('month', target_month);
    end_date   := start_date + interval '1 month';
    partition_name := 'credit_ledger_' || to_char(start_date, 'YYYY_MM');

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.credit_ledger FOR VALUES FROM (%L) TO (%L)',
        partition_name, start_date, end_date
    );

    RAISE NOTICE 'ensure_credit_ledger_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

DELETE FROM public.schema_migrations WHERE version = '694';

COMMIT;
