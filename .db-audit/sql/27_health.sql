export PGPAGER=cat
export TERM=dumb
echo "=== 内存 ==="
free -m | head -2
echo "=== 容器内 postmaster 资源 ==="
ps -eo pid,etime,rss,args 2>/dev/null | grep 'bin/postgres' | grep -v grep
echo "=== PG 当前内存占用（shared_buffers 实际）==="
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'shared_buffers='||current_setting('shared_buffers')||' | backends='||count(*) FROM pg_stat_activity;"
echo "=== 健康确认 ==="
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'uptime='||(now()-pg_postmaster_start_time())::text;"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'databases='||count(*) FROM pg_database WHERE datallowconn;"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'request_logs='||count(*) FROM request_logs;"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'app connections='||count(*) FROM pg_stat_activity WHERE datname='llm_gateway' AND application_name<>'psql';"
df -h /var/lib/postgresql/data | tail -1