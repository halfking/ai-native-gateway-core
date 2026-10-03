export PGPAGER=cat
export TERM=dumb
PSQL="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=0"

echo "===== BAK_* BACKUP TABLES TOTAL ====="
$PSQL -c "SELECT count(*)||' tables, total='||pg_size_pretty(sum(pg_total_relation_size(c.oid)))||', rows~'||sum(greatest(c.reltuples,0))::bigint FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='r' AND c.relname LIKE 'bak\_%';"

echo "===== ALL-NULL COLUMNS IN BIG TABLES (storage waste) ====="
$PSQL -c "SELECT s.tablename||' | '||s.attname||' | type='||format_type(a.atttypid,a.atttypmod)||' | attlen='||a.attlen||' | attstorage='||a.attstorage||' | attalign='||a.attalign FROM pg_stats s JOIN pg_attribute a ON a.attrelid=(quote_ident(s.schemaname)||'.'||quote_ident(s.tablename))::regclass AND a.attname=s.attname WHERE s.schemaname='public' AND s.null_frac=1.0 AND s.tablename IN ('request_logs_2026_09','session_turns_2026_09','sessions_2026_09','ursm_node_snapshot_min','request_logs_bodies_2026_09','session_turn_details_2026_09','request_state_transitions','session_dim') ORDER BY s.tablename, s.attname;"

echo "===== request_state_transitions INDEX BREAKDOWN ====="
$PSQL -c "SELECT indexrelname||' | '||pg_size_pretty(pg_relation_size(indexrelid))||' | idx_scan='||idx_scan||' | '||left(indexdef,130) FROM pg_stat_user_indexes WHERE relname='request_state_transitions' ORDER BY pg_relation_size(indexrelid) DESC;"

echo "===== request_state_transitions COLUMNS ====="
$PSQL -c "SELECT string_agg(column_name||' '||data_type, ', ' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='request_state_transitions';"

echo "===== ARCHIVE TABLES (columnar design) ====="
$PSQL -c "SELECT c.relname||' | '||pg_size_pretty(pg_total_relation_size(c.oid))||' | rows='||greatest(c.reltuples,0)::bigint||' | kind='||CASE c.relkind WHEN 'c' THEN 'COLUMNAR' WHEN 'p' THEN 'partitioned' WHEN 'r' THEN 'row' ELSE c.relkind::text END FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname LIKE '%archive%' ORDER BY pg_total_relation_size(c.oid) DESC;"

echo "===== COLUMNAR PARTITION CREATION FUNCTIONS ====="
$PSQL -c "SELECT p.proname||' | '||pg_get_function_identity_arguments(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prosrc LIKE '%USING columnar%' ORDER BY 1;"

echo "===== COLUMNAR GUCs ====="
$PSQL -c "SELECT name||' = '||setting||' ['||source||']' FROM pg_settings WHERE name LIKE 'columnar.%' ORDER BY 1;"

echo "===== ARCHIVE / RETENTION JOBS (functions mentioning archive or drop partition) ====="
$PSQL -c "SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND (p.prosrc ILIKE '%archive%' OR p.prosrc ILIKE '%DROP PARTITION%' OR p.prosrc ILIKE '%detach partition%') ORDER BY 1 LIMIT 30;"

echo "===== TOP 20 ALL-NULL-COLUMN COUNT PER BIG TABLE ====="
$PSQL -c "SELECT tablename||' | total_cols='||count(*)||' | all_null_cols='||count(*) FILTER (WHERE null_frac=1.0) FROM pg_stats WHERE schemaname='public' AND tablename IN ('request_logs_2026_09','session_turns_2026_09','sessions_2026_09','session_turn_details_2026_09','request_state_transitions','session_dim','route_incident_events','node_probe_runs') GROUP BY 1;"

echo "===== request_logs_2026_09 TOTAL WIDTH vs USED ====="
$PSQL -c "SELECT 'sum(avg_width) over non-null cols='||sum(avg_width)||' | row_count='||greatest((SELECT reltuples FROM pg_class WHERE relname='request_logs_2026_09'),0)::bigint||' | est bytes/row='||round(sum(avg_width)/8.0*1024)::bigint FROM pg_stats WHERE schemaname='public' AND tablename='request_logs_2026_09';"
$PSQL -c "SELECT 'heap='||pg_size_pretty(pg_relation_size('request_logs_2026_09'))||' + toast='||pg_size_pretty(pg_total_relation_size((SELECT reltoastrelid FROM pg_class WHERE relname='request_logs_2026_09')));"

echo "===== SESSION TURNS / REQUESTS VOLUME (rows per day) ====="
$PSQL -c "SELECT 'request_logs_2026_09 rows='||count(*) FROM request_logs_2026_09;"
$PSQL -c "SELECT 'session_turns_2026_09 rows='||count(*) FROM session_turns_2026_09;"
$PSQL -c "SELECT 'sessions_2026_09 rows='||count(*) FROM sessions_2026_09;"

echo "===== TABLE SIZE BUCKETS ====="
$PSQL -c "SELECT width_bucket(pg_total_relation_size(c.oid), 0, 6*1024*1024*1024, 6)||' | count='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='r' GROUP BY 1 ORDER BY 1;"

echo "===== TOTAL: heap vs index vs toast ====="
$PSQL -c "SELECT 'heap='||pg_size_pretty(sum(pg_relation_size(c.oid)))||' | index='||pg_size_pretty(sum(pg_indexes_size(c.oid)))||' | toast='||pg_size_pretty(sum(coalesce(pg_total_relation_size(c.reltoastrelid),0))) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p');"
