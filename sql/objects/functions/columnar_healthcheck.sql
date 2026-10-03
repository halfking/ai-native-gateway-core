--
-- Name: columnar_healthcheck(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.columnar_healthcheck() RETURNS TABLE(parent_name text, partition_name text, storage text, expected text, compliant boolean, total_size_bytes bigint, n_live_tup bigint)
    LANGUAGE sql STABLE
    AS $$
    WITH config AS (
        SELECT
            columnar_insert_only_parents() AS should_be_columnar,
            -- should_be_heap must cover every family that must NEVER be
            -- columnarised. Before 2026-10-01 it listed only 6 request_*/usage_*
            -- families, so the 20 families that actually caused the
            -- 2026-10-01 03:0x-06:44 outage (see
            -- docs/audit/2026-10-01-252-sql-log-audit-round17.md §〇/§一) fell
            -- through to expected='unknown', compliant=NULL.
            --
            -- Consequence of that gap: had any of them been converted again,
            -- this healthcheck would have reported 'unknown' rather than
            -- non-compliant — i.e. the detector for the recurrence was blind
            -- to exactly the families that recurred. The 20 names below are
            -- R17's 21-family list minus routing_decision_log (which is the
            -- canonical should_be_columnar).
            ARRAY['request_logs','request_wal','usage_ledger',
                  'request_logs_archive','request_wal_archive',
                  'usage_ledger_archive',
                  -- R17 rolled-back families (20):
                  'sessions','session_turns','session_turn_details',
                  'session_bodies','usage_facts','stats_event_inbox',
                  'credential_model_index','auto_route_selections',
                  'session_memora','session_censors','system_probe_runs',
                  'credit_ledger','tool_usage_stats','session_tools',
                  'session_module_executions','cache_metrics',
                  'dashboard_access_events','model_probe_runs','handoff_logs',
                  'supplier_errors']::text[] AS should_be_heap
    ), partitions AS (
        -- relkind filter is load-bearing: pg_inherits holds BOTH table
        -- partitions (relkind 'r') and the children of partitioned indexes
        -- (relkind 'i'). Without it, 641 of the 754 rows this function
        -- returned on 252 (2026-10-01) were index relations, reported with
        -- storage='other' / expected='unknown'. columnar_drift_report()
        -- aggregates over this, so their sizes were being summed as if they
        -- were table partitions. The parent must be a partitioned table ('p')
        -- and the child an ordinary one ('r').
        SELECT
            p.relname AS parent_name,
            c.relname AS partition_name,
            CASE WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='columnar') THEN 'columnar'
                 WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='heap') THEN 'heap'
                 ELSE 'other' END AS storage,
            pg_total_relation_size(c.oid) AS total_size_bytes,
            (SELECT n_live_tup FROM pg_stat_user_tables WHERE relid=c.oid) AS n_live_tup
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname = 'public'
          AND p.relkind = 'p'
          AND c.relkind = 'r'
    )
    SELECT
        par.parent_name,
        par.partition_name,
        par.storage,
        -- NOTE: 'unknown' is a legitimate THIRD state, not just a gap. Some
        -- families are columnarised per-partition by policy rather than being
        -- insert-only-enforced: on 252 (2026-10-01) request_logs_bodies
        -- _{2026_09,2026_10,2026_11} were all columnar while
        -- routing_decision_log_archive_default was columnar and its siblings
        -- heap. They are deliberately NOT in either list:
        --   * adding them to should_be_columnar means editing
        --     columnar_insert_only_parents(), which is the SSOT pinned by a
        --     contract test to exactly {routing_decision_log} — widening it is
        --     what caused the 2026-10-01 outage;
        --   * adding them to should_be_heap would flag their existing,
        --     intended columnar partitions as non-compliant.
        -- Residual 'unknown' families as of 2026-10-01 (14 partitions / 5
        -- parents): request_logs_bodies, routing_decision_log_archive,
        -- candidate_failure_logs, mock_probe_history,
        -- credential_model_index_archive. Closing that is a storage-policy
        -- decision, not a bug fix. What this function must never do is report
        -- the 20 R17 families as 'unknown' — that was the real gap.
        CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE 'unknown'
        END::text AS expected,
        (par.storage = CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE NULL END) AS compliant,
        par.total_size_bytes,
        COALESCE(par.n_live_tup, 0)
    FROM partitions par, config cfg
    ORDER BY par.parent_name, par.partition_name;
$$;

