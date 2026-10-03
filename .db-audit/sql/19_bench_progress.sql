export PGPAGER=cat
export TERM=dumb
P="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off"
echo "=== audit_scratch 里的对象（能看到进度）==="
$P -c "SELECT c.relname||' | '||CASE c.relkind WHEN 'r' THEN 'row' WHEN 'c' THEN 'COLUMNAR' ELSE c.relkind::text END||' | '||pg_size_pretty(pg_total_relation_size(c.oid)) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='audit_scratch' ORDER BY 1;"
echo "=== 当前在跑的语句 ==="
$P -c "SELECT pid||' | '||round(extract(epoch from now()-query_start))::int||'s | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),120) FROM pg_stat_activity WHERE datname='llm_gateway' AND state='active' AND now()-query_start > interval '5s' ORDER BY query_start LIMIT 5;"
echo "=== 磁盘 ==="
df -h /var/lib/postgresql/data | tail -1
