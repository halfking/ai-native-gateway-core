-- Run against the restored, isolated historical database before migrations.
-- Required psql variables: history_zone, closed_day, open_day.
\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '60s';
SET LOCAL lock_timeout = '2s';
SET LOCAL TIME ZONE 'UTC';

SELECT 'snapshot', jsonb_build_object(
    'database', current_database(), 'server_version', current_setting('server_version'),
    'read_only', current_setting('transaction_read_only'), 'isolation', current_setting('transaction_isolation'),
    'read_at', transaction_timestamp(), 'postgres_snapshot', pg_current_snapshot()::text
);

SELECT 'schema_ledger', jsonb_build_object(
    'rows', count(*),
    'sha256', encode(sha256(convert_to(COALESCE(string_agg(to_jsonb(m)::text || E'\n', ''
        ORDER BY version COLLATE "C"), ''), 'UTF8')), 'hex')
)
FROM public.schema_migrations m;

WITH files AS (
    SELECT sequence_name, content_sha256
    FROM public.gateway_db_revision_sequences
    WHERE sequence_name ~ '^[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+\.sql$'
)
SELECT 'deployment_ledger', jsonb_build_object(
    'file_rows', count(*),
    'fingerprinted_rows', count(*) FILTER (WHERE content_sha256 ~ '^[0-9a-f]{64}$'),
    'sha256', encode(sha256(convert_to(COALESCE(string_agg(sequence_name || '|' || content_sha256 || E'\n', ''
        ORDER BY sequence_name COLLATE "C") FILTER (WHERE content_sha256 ~ '^[0-9a-f]{64}$'), ''), 'UTF8')), 'hex')
)
FROM files;

SELECT 'storage', jsonb_build_object(
    'parent', target.name, 'exists', parent.oid IS NOT NULL, 'kind', parent.relkind,
    'leaf_partitions', COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'name', leaf.relname, 'bounds', pg_get_expr(leaf.relpartbound, leaf.oid),
            'access_method', am.amname
        ) ORDER BY ns.nspname, leaf.relname)
        FROM pg_partition_tree(parent.oid) tree
        JOIN pg_class leaf ON leaf.oid = tree.relid
        JOIN pg_namespace ns ON ns.oid = leaf.relnamespace
        LEFT JOIN pg_am am ON am.oid = leaf.relam
        WHERE tree.isleaf AND tree.level > 0
    ), '[]'::jsonb)
)
FROM (VALUES ('request_wal'), ('request_wal_archive'), ('credit_ledger'), ('usage_facts')) target(name)
LEFT JOIN pg_class parent ON parent.oid = to_regclass('public.' || target.name)
ORDER BY target.name;

WITH dates(ord, sample, day) AS (
    VALUES (1, 'closed_day', :'closed_day'::date), (2, 'open_day', :'open_day'::date)
), windows AS (
    SELECT ord, sample, day, day::timestamp AT TIME ZONE :'history_zone' AS start_at,
        (day + 1)::timestamp AT TIME ZONE :'history_zone' AS end_at
    FROM dates
)
SELECT 'raw_window', jsonb_build_object(
    'sample', sample, 'day', day, 'timezone', :'history_zone', 'start_at', start_at, 'end_at', end_at,
    'wal_rows', (SELECT count(*) FROM public.request_wal_with_current_month WHERE created_at >= start_at AND created_at < end_at),
    'usage_fact_rows', (SELECT count(*) FROM public.usage_facts WHERE occurred_at >= start_at AND occurred_at < end_at),
    'credit_ledger_rows', (SELECT count(*) FROM public.credit_ledger WHERE created_at >= start_at AND created_at < end_at),
    'credit_ledger_hot_rows', (SELECT count(*) FROM public.credit_ledger_hot WHERE created_at >= start_at AND created_at < end_at)
)
FROM windows
ORDER BY ord;

-- Optional accounting tables can be absent on a historical schema. Generate
-- only queries supported by the restored relations and their columns.
WITH sources(label, relation_name, columns, predicate) AS (
    VALUES
        ('projected_requests', 'request_metrics', ARRAY['occurred_at', 'source_fact_id'],
            $$e.occurred_at >= w.start_at AND e.occurred_at < w.end_at AND e.source_fact_id IS NOT NULL$$),
        ('confirmed_supplier_sends', 'provider_call_attempts', ARRAY['sent_at', 'delivery'],
            $$e.sent_at >= w.start_at AND e.sent_at < w.end_at AND e.delivery = 'sent'$$),
        ('overlapping_supplier_invoices', 'provider_invoices', ARRAY['window_start', 'window_end'],
            $$e.window_start < w.end_at AND e.window_end > w.start_at$$),
        ('overlapping_supplier_contract_rates', 'supplier_contract_rates', ARRAY['effective_from', 'effective_to'],
            $$e.effective_from < w.end_at AND (e.effective_to IS NULL OR e.effective_to > w.start_at)$$)
), relations AS (
    SELECT sources.*, to_regclass('public.' || relation_name) AS relation_oid
    FROM sources
), inventory AS (
    SELECT relations.*, ARRAY(
        SELECT required.name
        FROM unnest(columns) required(name)
        WHERE NOT EXISTS (
            SELECT 1 FROM pg_attribute a
            WHERE a.attrelid = relation_oid AND a.attname = required.name AND a.attnum > 0 AND NOT a.attisdropped
        )
        ORDER BY required.name
    ) AS missing_columns
    FROM relations
), classified AS (
    SELECT inventory.*, CASE
        WHEN relation_oid IS NULL THEN 'missing_relation'
        WHEN cardinality(missing_columns) > 0 THEN 'missing_columns'
        WHEN EXISTS (
            SELECT 1 FROM unnest(columns) required(name)
            WHERE NOT has_column_privilege(relation_oid, required.name, 'SELECT')
        ) THEN 'unreadable'
        ELSE 'counted'
    END AS schema_state
    FROM inventory
)
SELECT format($query$
    WITH windows(sample, day, start_at, end_at) AS (
        SELECT sample, day, day::timestamp AT TIME ZONE %1$L, (day + 1)::timestamp AT TIME ZONE %1$L
        FROM (VALUES ('closed_day', %2$L::date), ('open_day', %3$L::date)) dates(sample, day)
    )
    SELECT 'accounting_source_window', jsonb_build_object(
        'sample', w.sample, 'day', w.day, 'start_at', w.start_at, 'end_at', w.end_at,
        'source', %4$L, 'relation', %5$L, 'schema_state', %6$L, 'missing_columns', %7$L::text[],
        'rls_active', %8$L::boolean, 'rows', %9$s
    )
    FROM windows w ORDER BY w.sample;
$query$, :'history_zone', :'closed_day', :'open_day', label, relation_name, schema_state, missing_columns,
    row_security_active(relation_oid),
    CASE WHEN schema_state = 'counted' THEN format('(SELECT count(*) FROM public.%I e WHERE %s)', relation_name, predicate)
        ELSE 'NULL::bigint' END)
FROM classified
ORDER BY label
\gexec
COMMIT;
