export PGPAGER=cat
export TERM=dumb
PSQL="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=0"

echo "===== PGSS SUMMARY ====="
$PSQL -c "SELECT count(*)||' statements, total_exec='||round((sum(total_exec_time)/1000.0)::numeric,1)||'s, total_plan='||round((sum(total_plan_time)/1000.0)::numeric,1)||'s, rows='||sum(rows) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway');"

echo "===== TOP 30 BY MEAN EXEC TIME (calls>=20) ====="
$PSQL -c "SELECT round(mean_exec_time::numeric,1)||' ms | calls='||calls||' | total='||round((total_exec_time/1000.0)::numeric,1)||'s | rows='||rows||' | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),160) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') AND calls>=20 ORDER BY mean_exec_time DESC LIMIT 30;"

echo "===== TOP 30 BY TOTAL EXEC TIME ====="
$PSQL -c "SELECT round((total_exec_time/1000.0)::numeric,1)||'s | mean='||round(mean_exec_time::numeric,1)||'ms | calls='||calls||' | rows='||rows||' | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),160) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') ORDER BY total_exec_time DESC LIMIT 30;"

echo "===== WRITE HEAVY (INSERT/UPDATE/DELETE) BY TOTAL ====="
$PSQL -c "SELECT round((total_exec_time/1000.0)::numeric,1)||'s | mean='||round(mean_exec_time::numeric,1)||'ms | calls='||calls||' | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),150) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') AND query ~* '^(insert|update|delete)' ORDER BY total_exec_time DESC LIMIT 15;"

echo "===== HIGHEST CALL VOLUME (top 15) ====="
$PSQL -c "SELECT calls||' calls | total='||round((total_exec_time/1000.0)::numeric,1)||'s | mean='||round(mean_exec_time::numeric,2)||'ms | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),120) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') ORDER BY calls DESC LIMIT 15;"

echo "===== ASSETS TABLE ANOMALY ====="
$PSQL -c "SELECT 'assets columns: '||string_agg(column_name||' '||data_type, ', ' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='assets';"
$PSQL -c "SELECT 'assets indexes: '||count(*) FROM pg_indexes WHERE tablename='assets';"
$PSQL -c "SELECT indexdef FROM pg_indexes WHERE tablename='assets';"

echo "===== TOAST COMPRESSION AUDIT (top toast tables) ====="
$PSQL -c "SELECT c.relname||' | toast='||pg_size_pretty(pg_total_relation_size(c.reltoastrelid))||' | attstorage/toasttuple='||t2.attstorage||'/'||t2.attcompression||' | avg_width='||coalesce(s.avg_width::text,'-')||' | null_frac='||coalesce(s.null_frac::text,'-') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_class t2 ON t2.oid=c.reltoastrelid LEFT JOIN pg_stats s ON s.schemaname=n.nspname AND s.tablename=c.relname AND s.attname='outbound_body' WHERE n.nspname='public' AND c.reltoastrelid<>0 AND pg_total_relation_size(c.reltoastrelid) > 10*1024*1024 ORDER BY pg_total_relation_size(c.reltoastrelid) DESC LIMIT 15;"

echo "===== COLUMN WIDTH / NULLRATE: request_logs ====="
$PSQL -c "SELECT attname||' | '||null_frac||' | '||avg_width||' | '||n_distinct FROM pg_stats WHERE schemaname='public' AND tablename='request_logs' ORDER BY avg_width DESC NULLS LAST LIMIT 25;"
echo "===== COLUMN WIDTH / NULLRATE: session_bodies ====="
$PSQL -c "SELECT attname||' | '||null_frac||' | '||avg_width||' | '||n_distinct FROM pg_stats WHERE schemaname='public' AND tablename='session_bodies' ORDER BY avg_width DESC NULLS LAST LIMIT 20;"
echo "===== COLUMN WIDTH / NULLRATE: ursm_node_snapshot_min ====="
$PSQL -c "SELECT attname||' | '||null_frac||' | '||avg_width||' | '||n_distinct FROM pg_stats WHERE schemaname='public' AND tablename='ursm_node_snapshot_min' ORDER BY avg_width DESC NULLS LAST LIMIT 20;"

echo "===== CONNECTIONS ====="
$PSQL -c "SELECT usename||' | '||coalesce(state,'-')||' | '||count(*) FROM pg_stat_activity WHERE datname='llm_gateway' GROUP BY usename, state ORDER BY count(*) DESC;"
echo "===== WAIT EVENTS ====="
$PSQL -c "SELECT wait_event_type||'/'||coalesce(wait_event,'-')||' = '||count(*) FROM pg_stat_activity WHERE wait_event_type IS NOT NULL GROUP BY wait_event_type, wait_event ORDER BY count(*) DESC LIMIT 12;"

echo "===== UNUSED / UNUSED-SIZE INDEXES (top 30) ====="
$PSQL -c "SELECT ui.relname||' | table='||ui.relname||' | size='||pg_size_pretty(pg_relation_size(ui.indexrelid))||' | idx_scan='||ui.idx_scan||' | '||left(ui.indexdef,120) FROM pg_stat_user_indexes ui JOIN pg_index i ON i.indexrelid=ui.indexrelid JOIN pg_class c ON c.oid=i.indrelid WHERE ui.idx_scan=0 AND NOT i.indisunique AND NOT i.indisprimary AND pg_relation_size(ui.indexrelid) > 1024*1024 ORDER BY pg_relation_size(ui.indexrelid) DESC LIMIT 30;"

echo "===== INDEXES WITH LOW SELECTIVITY (idx_tup_read >> rows) ====="
$PSQL -c "SELECT s.relname||' | idx_scan='||s.idx_scan||' | idx_tup_read='||s.idx_tup_fetch||' | ratio='||round(s.idx_tup_fetch::numeric/nullif(s.idx_scan,0),1)||' | n_live='||s.n_live_tup FROM pg_stat_user_tables s WHERE s.idx_scan>1000 ORDER BY (s.idx_tup_fetch::numeric/nullif(s.idx_scan,0)) DESC LIMIT 15;"
