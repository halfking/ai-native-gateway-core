export PGPAGER=cat
export TERM=dumb
PSQL="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=0"

echo "===== RELKIND BREAKDOWN (columnar check) ====="
$PSQL -c "SELECT CASE c.relkind WHEN 'r' THEN 'ordinary table' WHEN 'p' THEN 'partitioned table' WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview' WHEN 'S' THEN 'sequence' WHEN 'i' THEN 'index' WHEN 'c' THEN 'COLUMNAR' WHEN 'f' THEN 'foreign' ELSE c.relkind::text END||' = '||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' GROUP BY 1 ORDER BY 2 DESC;"

echo "===== COLUMNS: request_logs_2026_09 (avg_width / null_frac) ====="
$PSQL -c "SELECT attname||' | null_frac='||null_frac||' | avg_width='||avg_width||' | n_distinct='||n_distinct FROM pg_stats WHERE schemaname='public' AND tablename='request_logs_2026_09' ORDER BY avg_width DESC NULLS LAST LIMIT 30;"

echo "===== COLUMNS: session_bodies_2026_09 ====="
$PSQL -c "SELECT attname||' | null_frac='||null_frac||' | avg_width='||avg_width||' | n_distinct='||n_distinct FROM pg_stats WHERE schemaname='public' AND tablename='session_bodies_2026_09' ORDER BY avg_width DESC NULLS LAST LIMIT 20;"

echo "===== COLUMNS: request_logs_bodies_2026_09 ====="
$PSQL -c "SELECT attname||' | null_frac='||null_frac||' | avg_width='||avg_width||' FROM pg_stats WHERE schemaname='public' AND tablename='request_logs_bodies_2026_09' ORDER BY avg_width DESC NULLS LAST LIMIT 12;"

echo "===== COLUMNS: session_turns_2026_09 ====="
$PSQL -c "SELECT attname||' | null_frac='||null_frac||' | avg_width='||avg_width||' | n_distinct='||n_distinct FROM pg_stats WHERE schemaname='public' AND tablename='session_turns_2026_09' ORDER BY avg_width DESC NULLS LAST LIMIT 20;"

echo "===== TOAST COMPRESSION: per-table toast size & attcompression ====="
$PSQL -c "SELECT c.relname||' | toast='||pg_size_pretty(pg_total_relation_size(c.reltoastrelid))||' | attcompression='||a.attcompression||' | attstorage='||a.attstorage||' | column='||a.attname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 LEFT JOIN pg_class t ON t.oid=c.reltoastrelid WHERE n.nspname='public' AND c.reltoastrelid<>0 AND a.attstorage='x' AND pg_total_relation_size(c.reltoastrelid) > 20*1024*1024 ORDER BY pg_total_relation_size(c.reltoastrelid) DESC LIMIT 20;"

echo "===== ALL EVER-TOASTED COLUMNS + compression method ====="
$PSQL -c "SELECT a.attcompression||' = '||count(*) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND a.attstorage='x' AND a.attnum>0 GROUP BY 1;"

echo "===== UNUSED INDEXES >1MB (non-unique) ====="
$PSQL -c "SELECT ic.relname||' | table='||tc.relname||' | size='||pg_size_pretty(pg_relation_size(ic.oid))||' | idx_scan='||ui.idx_scan||' | def='||left(pg_get_indexdef(ic.oid),110) FROM pg_stat_user_indexes ui JOIN pg_index i ON i.indexrelid=ui.indexrelid JOIN pg_class ic ON ic.oid=i.indexrelid JOIN pg_class tc ON tc.oid=i.indrelid WHERE ui.idx_scan=0 AND NOT i.indisunique AND pg_relation_size(ic.oid) > 1048576 ORDER BY pg_relation_size(ic.oid) DESC LIMIT 25;"

echo "===== TOTAL SIZE OF UNUSED INDEXES ====="
$PSQL -c "SELECT pg_size_pretty(sum(pg_relation_size(ic.oid)))||' across '||count(*)||' unused non-unique indexes >1MB' FROM pg_stat_user_indexes ui JOIN pg_index i ON i.indexrelid=ui.indexrelid JOIN pg_class ic ON ic.oid=i.indexrelid WHERE ui.idx_scan=0 AND NOT i.indisunique AND pg_relation_size(ic.oid) > 1048576;"

echo "===== PARTITION INVENTORY: partitioned parents ====="
$PSQL -c "SELECT p.relname||' | children='||count(c.oid)||' | strategy='||CASE p.partstrat WHEN 'r' THEN 'RANGE' WHEN 'l' THEN 'LIST' WHEN 'h' THEN 'HASH' ELSE p.partstrat::text END||' | key='||pg_get_partkeydef(p.oid) FROM pg_class p JOIN pg_namespace n ON n.oid=p.relnamespace LEFT JOIN pg_inherits i ON i.inhparent=p.oid LEFT JOIN pg_class c ON c.oid=i.inhrelid WHERE n.nspname='public' AND p.relkind='p' GROUP BY p.relname,p.partstrat,p.oid ORDER BY count(c.oid) DESC;"

echo "===== PARTITION COUNT TOTAL ====="
$PSQL -c "SELECT count(*) FROM pg_inherits;"

echo "===== CROSS-SCHEMA USAGE ====="
$PSQL -c "SELECT n.nspname||' = '||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','p','v','m') GROUP BY 1 ORDER BY 2 DESC;"

echo "===== MV REFRESH FREQUENCY ====="
$PSQL -c "SELECT schemaname||'.'||matviewname||' | size='||pg_size_pretty(pg_total_relation_size((quote_ident(schemaname)||'.'||quote_ident(matviewname))::regclass)) FROM pg_matviews WHERE schemaname='public' ORDER BY pg_total_relation_size((quote_ident(schemaname)||'.'||quote_ident(matviewname))::regclass) DESC;"

echo "===== SEQUENCES ====="
$PSQL -c "SELECT count(*)||' sequences' FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='S';"
