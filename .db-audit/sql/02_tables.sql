export PGPAGER=cat
export TERM=dumb
PSQL="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=0"

echo "===== STATS RESET AGE ====="
$PSQL -c "SELECT 'pg_stat_database.reset='||stats_reset||' now='||now() FROM pg_stat_database WHERE datname='llm_gateway';"
$PSQL -c "SELECT 'checkpoint stats='||num_timed||' timed, '||num_requested||' requested, write_time='||write_time||'ms, sync_time='||sync_time||'ms, buffers_written='||buffers_written||', stats_reset='||stats_reset FROM pg_stat_checkpointer;"

echo "===== TOP 60 TABLES BY TOTAL SIZE ====="
$PSQL -c "SELECT c.relname||' | '||pg_size_pretty(pg_total_relation_size(c.oid))||' | heap='||pg_size_pretty(pg_relation_size(c.oid))||' | toast='||pg_size_pretty(coalesce(pg_total_relation_size(c.reltoastrelid),0))||' | idx='||pg_size_pretty(pg_indexes_size(c.oid))||' | est_rows='||greatest(c.reltuples,0)::bigint||' | rk='||c.relkind::text||' | parent='||coalesce(p.relname,'-') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_inherits i ON i.inhrelid=c.oid LEFT JOIN pg_class p ON p.oid=i.inhparent WHERE n.nspname='public' AND c.relkind IN ('r','p') ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 60;"

echo "===== TOP 30 PARTITIONS BY SIZE (leaf) ====="
$PSQL -c "SELECT c.relname||' | '||pg_size_pretty(pg_total_relation_size(c.oid))||' | est_rows='||greatest(c.reltuples,0)::bigint||' | parent='||coalesce(p.relname,'-') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_inherits i ON i.inhrelid=c.oid LEFT JOIN pg_class p ON p.oid=i.inhparent WHERE n.nspname='public' AND c.relkind='r' AND EXISTS(SELECT 1 FROM pg_inherits x WHERE x.inhrelid=c.oid) ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 30;"

echo "===== INDEX TOTALS BY TABLE (top 30 idx size) ====="
$PSQL -c "SELECT c.relname||' | indexes='||pg_size_pretty(pg_indexes_size(c.oid))||' | heap='||pg_size_pretty(pg_relation_size(c.oid))||' | ratio='||round(100.0*pg_indexes_size(c.oid)/nullif(pg_total_relation_size(c.oid),0),1)||'%' FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND pg_indexes_size(c.oid) > 0 ORDER BY pg_indexes_size(c.oid) DESC LIMIT 30;"

echo "===== INDEX ACCESS SCANS (top 40) ====="
$PSQL -c "SELECT s.relname||' | idx_scan='||s.idx_scan||' | idx_tup_read='||s.idx_tup_fetch||' | seq_scan='||s.seq_scan||' | seq_tup_read='||s.seq_tup_read||' | rows='||greatest(s.reltuples,0)::bigint||' | size='||pg_size_pretty(pg_total_relation_size(s.relid)) FROM pg_stat_user_tables s WHERE s.seq_scan+s.idx_scan > 0 ORDER BY pg_total_relation_size(s.relid) DESC LIMIT 40;"

echo "===== HIGHEST SEQ_SCAN (top 25, excluding tiny) ====="
$PSQL -c "SELECT s.relname||' | seq_scan='||s.seq_scan||' | seq_tup_read='||s.seq_tup_read||' | idx_scan='||s.idx_scan||' | rows='||greatest(s.reltuples,0)::bigint||' | size='||pg_size_pretty(pg_total_relation_size(s.relid)) FROM pg_stat_user_tables s WHERE s.seq_scan>0 ORDER BY s.seq_tup_read DESC LIMIT 25;"

echo "===== DEAD TUPLES / BLOAT CANDIDATES (top 25) ====="
$PSQL -c "SELECT s.relname||' | n_live='||n_live||' | n_dead='||n_dead||' | dead_pct='||round(100.0*n_dead/nullif(n_live+n_dead,0),1)||'%' ||' | autovac='||coalesce(s.autovacuum_count,0)||' | autoan='||coalesce(s.autoanalyze_count,0)||' | last_vac='||coalesce(s.last_vacuum::text,'-')||' | last_auto_vac='||coalesce(s.last_autovacuum::text,'-')||' | size='||pg_size_pretty(pg_total_relation_size(s.relid)) FROM pg_stat_user_tables s WHERE n_live+n_dead > 0 ORDER BY n_dead DESC LIMIT 25;"

echo "===== TABLES NEVER ANALYZED ====="
$PSQL -c "SELECT count(*) FROM pg_stat_user_tables WHERE last_analyze IS NULL AND last_autoanalyze IS NULL;"
$PSQL -c "SELECT relname||' | rows='||greatest(n_live_tup,0) FROM pg_stat_user_tables WHERE last_analyze IS NULL AND last_autoanalyze IS NULL AND n_live_tup>100 ORDER BY n_live_tup DESC LIMIT 20;"
