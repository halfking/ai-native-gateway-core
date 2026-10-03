export PGPAGER=cat
export TERM=dumb
PSQL="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=0"

echo "===== RELKIND BREAKDOWN (columnar check) ====="
$PSQL -c "SELECT CASE c.relkind WHEN 'r' THEN 'ordinary_table' WHEN 'p' THEN 'partitioned_table' WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview' WHEN 'S' THEN 'sequence' WHEN 'i' THEN 'index' WHEN 'c' THEN 'COLUMNAR' WHEN 'f' THEN 'foreign' ELSE c.relkind::text END AS kind, count(*) AS cnt FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' GROUP BY 1 ORDER BY cnt DESC;"

echo "===== TOAST COMPRESSION METHODS (ever-toasted cols) ====="
$PSQL -c "SELECT a.attcompression::text AS comp, count(*) AS cnt FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND a.attstorage='x' AND a.attnum>0 GROUP BY 1;"

echo "===== BIGGEST TOASTED TABLES: storage detail ====="
$PSQL -c "SELECT c.relname||' | toast='||pg_size_pretty(pg_total_relation_size(c.reltoastrelid))||' | col='||a.attname||' | comp='||a.attcompression::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 WHERE n.nspname='public' AND c.reltoastrelid<>0 AND a.attstorage='x' AND pg_total_relation_size(c.reltoastrelid) > 20*1024*1024 ORDER BY pg_total_relation_size(c.reltoastrelid) DESC LIMIT 18;"

echo "===== TOTAL TOAST SPACE ====="
$PSQL -c "SELECT pg_size_pretty(sum(pg_total_relation_size(c.reltoastrelid))) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.reltoastrelid<>0;"

echo "===== PARTITION INVENTORY ====="
$PSQL -c "SELECT p.relname||' | children='||count(c.oid) FROM pg_class p JOIN pg_namespace n ON n.oid=p.relnamespace JOIN pg_inherits i ON i.inhparent=p.oid JOIN pg_class c ON c.oid=i.inhrelid WHERE n.nspname='public' AND p.relkind='p' GROUP BY p.relname ORDER BY count(c.oid) DESC;"

echo "===== PARTITION KEY DETAIL ====="
$PSQL -c "SELECT p.relname||' | '||pg_get_partkeydef(p.oid) FROM pg_class p JOIN pg_namespace n ON n.oid=p.relnamespace WHERE n.nspname='public' AND p.relkind='p' ORDER BY p.relname;"

echo "===== SCHEMAS / CROSS-SCHEMA OBJECTS ====="
$PSQL -c "SELECT n.nspname AS schema, count(*) AS cnt FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','p','v','m') GROUP BY 1 ORDER BY cnt DESC;"

echo "===== request_logs_bodies_2026_09 COLUMNS ====="
$PSQL -c "SELECT attname||' | null_frac='||null_frac||' | avg_width='||avg_width FROM pg_stats WHERE schemaname='public' AND tablename='request_logs_bodies_2026_09' ORDER BY avg_width DESC NULLS LAST LIMIT 12;"

echo "===== PARTITION RETENTION: monthly partitions present per parent ====="
$PSQL -c "SELECT p.relname||' -> '||string_agg(c.relname, ', ' ORDER BY c.relname) FROM pg_class p JOIN pg_namespace n ON n.oid=p.relnamespace JOIN pg_inherits i ON i.inhparent=p.oid JOIN pg_class c ON c.oid=i.inhrelid WHERE n.nspname='public' AND p.relkind='p' GROUP BY p.relname;"

echo "===== ROW COUNTS: exact for top tables ====="
$PSQL -c "SELECT 'ursm_node_snapshot_min='||count(*) FROM ursm_node_snapshot_min;"
$PSQL -c "SELECT 'request_logs_bodies_2026_09='||count(*) FROM request_logs_bodies_2026_09;"
$PSQL -c "SELECT 'assets='||count(*) FROM assets;"
$PSQL -c "SELECT 'providers='||count(*) FROM providers;"
$PSQL -c "SELECT 'provider_models='||count(*) FROM provider_models;"
$PSQL -c "SELECT 'models_canonical='||count(*) FROM models_canonical;"
$PSQL -c "SELECT 'credential_model_bindings='||count(*) FROM credential_model_bindings;"
$PSQL -c "SELECT 'credential_probe_queue='||count(*) FROM credential_probe_queue;"

echo "===== TIME RANGE OF request_logs ====="
$PSQL -c "SELECT 'request_logs min='||min(ts)||' max='||max(ts) FROM request_logs;"
$PSQL -c "SELECT 'session_bodies min='||min(ts)||' max='||max(ts) FROM session_bodies;"
$PSQL -c "SELECT 'ursm min='||min(snapshot_ts)||' max='||max(snapshot_ts) FROM ursm_node_snapshot_min;"
