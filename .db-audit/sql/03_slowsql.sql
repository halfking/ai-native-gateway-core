export PGPAGER=cat
export TERM=dumb
PSQL="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=0"

echo "===== PG_STAT_STATEMENTS: SUMMARY ====="
$PSQL -c "SELECT count(*)||' statements, total_exec='||round(sum(total_exec_time)/1000.0,1)||'s, total_plan='||round(sum(total_plan_time)/1000.0,1)||'s' FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway');"
$PSQL -c "SELECT 'stats_reset='||stats_reset FROM pg_stat_statements_info;"

echo "===== TOP 25 BY MEAN EXEC TIME (calls>=20) ====="
$PSQL -c "SELECT round(mean_exec_time::numeric,1)||' ms | calls='||calls||' | total='||round(total_exec_time/1000.0,1)||'s | rows='||rows||' | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),150) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') AND calls>=20 ORDER BY mean_exec_time DESC LIMIT 25;"

echo "===== TOP 25 BY TOTAL EXEC TIME ====="
$PSQL -c "SELECT round(total_exec_time/1000.0,1)||'s | mean='||round(mean_exec_time::numeric,1)||'ms | calls='||calls||' | rows='||rows||' | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),150) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') ORDER BY total_exec_time DESC LIMIT 25;"

echo "===== TOP 20 BY ROWS RETURNED ====="
$PSQL -c "SELECT rows||' rows | calls='||calls||' | mean='||round(mean_exec_time::numeric,1)||'ms | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),130) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') ORDER BY rows DESC LIMIT 20;"

echo "===== SHARED-BLOCK-I/O HEAVY (shared_blks_read) ====="
$PSQL -c "SELECT shared_blks_read||' blks_read | calls='||calls||' | mean='||round(mean_exec_time::numeric,1)||'ms | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),130) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') ORDER BY shared_blks_read DESC LIMIT 20;"

echo "===== TEMP SPILLING QUERIES (temp_blks_written) ====="
$PSQL -c "SELECT temp_blks_written||' temp_blks | calls='||calls||' | mean='||round(mean_exec_time::numeric,1)||'ms | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),130) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') ORDER BY temp_blks_written DESC LIMIT 15;"

echo "===== WRITE HEAVY (INSERT/UPDATE) ====="
$PSQL -c "SELECT queryid||' | calls='||calls||' | total='||round(total_exec_time/1000.0,1)||'s | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),140) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway') AND query ~* '^(insert|update|delete)' ORDER BY total_exec_time DESC LIMIT 15;"

echo "===== JIT / WAL / TRIGGERS ====="
$PSQL -c "SELECT 'jit_compiles='||sum(jit_functions) FILTER (WHERE jit_functions>0)||' on '||count(*) FILTER (WHERE jit_functions>0)||' stmts' FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway');"
$PSQL -c "SELECT 'wal_bytes='||pg_size_pretty(sum(wal_bytes))||' across '||count(*)||' stmts' FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway');"
$PSQL -c "SELECT round(sum(blk_read_time)/1000.0,1)||'s blk_read_time, '||round(sum(blk_write_time)/1000.0,1)||'s blk_write_time' FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway');"

echo "===== TABLE SCAN BEHAVIOUR (fixed) ====="
$PSQL -c "SELECT s.relname||' | idx_scan='||s.idx_scan||' | idx_tup_read='||s.idx_tup_fetch||' | seq_scan='||s.seq_scan||' | seq_tup_read='||s.seq_tup_read||' | n_live='||s.n_live_tup||' | size='||pg_size_pretty(pg_total_relation_size(s.relid)) FROM pg_stat_user_tables s WHERE s.seq_scan+s.idx_scan>0 ORDER BY pg_total_relation_size(s.relid) DESC LIMIT 30;"
$PSQL -c "SELECT s.relname||' | seq_scan='||s.seq_scan||' | seq_tup_read='||s.seq_tup_read||' | idx_scan='||s.idx_scan||' | n_live='||s.n_live_tup||' | size='||pg_size_pretty(pg_total_relation_size(s.relid)) FROM pg_stat_user_tables s WHERE s.seq_scan>0 ORDER BY s.seq_tup_read DESC LIMIT 20;"

echo "===== DEAD TUPLES / VACUUM ====="
$PSQL -c "SELECT s.relname||' | n_live='||s.n_live_tup||' | n_dead='||s.n_dead_tup||' | dead_pct='||round(100.0*s.n_dead_tup/nullif(s.n_live_tup+s.n_dead_tup,0),1)||'% | autovac='||coalesce(s.autovacuum_count,0)||' | autoan='||coalesce(s.autoanalyze_count,0)||' | last_vac='||coalesce(s.last_vacuum::text,'-')||' | last_autovac='||coalesce(s.last_autovacuum::text,'-')||' | size='||pg_size_pretty(pg_total_relation_size(s.relid)) FROM pg_stat_user_tables s WHERE s.n_live_tup+s.n_dead_tup>0 ORDER BY s.n_dead_tup DESC LIMIT 25;"

echo "===== USER CONNECTIONS ====="
$PSQL -c "SELECT usename||' | '||state||' | count='||count(*) FROM pg_stat_activity WHERE datname='llm_gateway' GROUP BY 1,2 ORDER BY 3 DESC;"
$PSQL -c "SELECT 'total_backends='||count(*) FROM pg_stat_activity;"
$PSQL -c "SELECT 'max_conn='||current_setting('max_connections')||' reserved_by_superuser='||count(*) FILTER (WHERE backend_type='superuser reserved connection' OR backend_type='reserved connection') FROM pg_stat_activity;"

echo "===== LOCKS / WAIT EVENTS ====="
$PSQL -c "SELECT 'locks='||count(*) FROM pg_locks;"
$PSQL -c "SELECT wait_event_type||'/'||coalesce(wait_event,'-')||' = '||count(*) FROM pg_stat_activity WHERE wait_event_type IS NOT NULL GROUP BY 1 ORDER BY 2 DESC LIMIT 12;"
