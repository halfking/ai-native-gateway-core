export PGPAGER=cat
export TERM=dumb
PSQL="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=0"

echo "===== ALL-NULL COLUMNS IN BIG TABLES ====="
$PSQL -c "SELECT s.tablename||' | '||s.attname||' | type='||format_type(a.atttypid,a.atttypmod)||' | attlen='||a.attlen::text||' | align='||a.attalign::text FROM pg_stats s JOIN pg_attribute a ON a.attrelid=(quote_ident(s.schemaname)||'.'||quote_ident(s.tablename))::regclass AND a.attname=s.attname WHERE s.schemaname='public' AND s.null_frac=1.0 AND s.tablename IN ('request_logs_2026_09','session_turns_2026_09','sessions_2026_09','ursm_node_snapshot_min','request_state_transitions','session_dim','route_incident_events','session_turn_details_2026_09','request_logs_bodies_2026_09') ORDER BY s.tablename, s.attname;"

echo "===== COL COUNT / ALL-NULL COUNT PER BIG TABLE ====="
$PSQL -c "SELECT t||' | total_cols='||tc||' | all_null='||an FROM (SELECT tablename AS t, count(*) AS tc, count(*) FILTER (WHERE null_frac=1.0) AS an FROM pg_stats WHERE schemaname='public' AND tablename IN ('request_logs_2026_09','session_turns_2026_09','sessions_2026_09','session_turn_details_2026_09','request_state_transitions','session_dim','route_incident_events','node_probe_runs','analysis_events','request_stage_events','stats_event_inbox_default') GROUP BY tablename) x ORDER BY an DESC;"

echo "===== request_state_transitions INDEX BREAKDOWN ====="
$PSQL -c "SELECT i.relname||' | '||pg_size_pretty(pg_relation_size(i.oid))||' | idx_scan='||s.idx_scan||' | '||left(pg_get_indexdef(i.oid),120) FROM pg_class i JOIN pg_index x ON x.indexrelid=i.oid JOIN pg_class t ON t.oid=x.indrelid JOIN pg_stat_user_indexes s ON s.indexrelid=i.oid WHERE t.relname='request_state_transitions' ORDER BY pg_relation_size(i.oid) DESC;"

echo "===== TABLE SIZE BUCKETS ====="
$PSQL -c "SELECT CASE WHEN pg_total_relation_size(c.oid) < 10*1024*1024 THEN 'a <10MB' WHEN pg_total_relation_size(c.oid) < 100*1024*1024 THEN 'b 10-100MB' WHEN pg_total_relation_size(c.oid) < 1024*1024*1024 THEN 'c 100MB-1GB' WHEN pg_total_relation_size(c.oid) < 4*1024*1024*1024 THEN 'd 1-4GB' ELSE 'e >4GB' END AS bucket, count(*) AS cnt FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='r' GROUP BY 1 ORDER BY 1;"

echo "===== assets: what is written? ====="
$PSQL -c "SELECT kind||' = '||count(*) FROM assets GROUP BY 1 ORDER BY 2 DESC LIMIT 10;"
$PSQL -c "SELECT 'max version churn: '||relname FROM pg_stat_user_tables WHERE relname='assets';"
$PSQL -c "SELECT 'assets size='||pg_size_pretty(pg_total_relation_size('assets'))||' heap='||pg_size_pretty(pg_relation_size('assets'))||' idx='||pg_size_pretty(pg_indexes_size('assets'))||' toast='||pg_size_pretty(coalesce(pg_total_relation_size((SELECT reltoastrelid FROM pg_class WHERE relname='assets')),0));"

echo "===== HEAVY-WRITE TABLE INSERT RATES (4-day window) ====="
$PSQL -c "SELECT relname||' | ins='||n_tup_ins||' | upd='||n_tup_upd||' | del='||n_tup_del||' | hot_upd='||n_tup_hot_upd FROM pg_stat_user_tables WHERE n_tup_ins+n_tup_upd+n_tup_del > 500000 ORDER BY (n_tup_ins+n_tup_upd+n_tup_del) DESC LIMIT 20;"
