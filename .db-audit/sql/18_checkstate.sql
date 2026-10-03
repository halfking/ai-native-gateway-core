export PGPAGER=cat
export TERM=dumb
P="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off"
echo "===== long running / active queries ====="
$P -c "SELECT pid||' | '||round(extract(epoch from now()-query_start))::int||'s | '||state||' | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),150) FROM pg_stat_activity WHERE datname='llm_gateway' AND state<>'idle' AND now()-query_start > interval '20s' ORDER BY query_start;"
echo "===== audit_scratch objects (any) ====="
$P -c "SELECT n.nspname||'.'||c.relname||' | '||c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='audit_scratch';"
echo "===== columnar table count (unchanged?) ====="
$P -c "SELECT 'columnar tables = '||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='c';"
echo "===== idle-in-transaction sessions (rolled back cleanly?) ====="
$P -c "SELECT state||' = '||count(*) FROM pg_stat_activity WHERE datname='llm_gateway' AND state LIKE '%idle in transaction%';"
