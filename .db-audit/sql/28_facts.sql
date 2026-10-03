export PGPAGER=cat
export TERM=dumb
echo "=== postmaster 权威信息 ==="
echo "postmaster.pid line1 = $(head -1 /var/lib/postgresql/data/postmaster.pid 2>/dev/null)"
echo "postmaster.pid line2 (start time) = $(sed -n '2p' /var/lib/postgresql/data/postmaster.pid 2>/dev/null)"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'pg_postmaster_start_time = '||pg_postmaster_start_time();"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'my_backend_pid = '||pg_backend_pid();"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT name||' = '||setting||coalesce(unit,'')||'  ['||source||']' FROM pg_settings WHERE name IN ('shared_buffers','work_mem','max_connections','port','data_directory');"
echo ""
echo "=== 该 pid 是什么 ==="
PMPID=$(head -1 /var/lib/postgresql/data/postmaster.pid 2>/dev/null)
ps -o pid,ppid,etime,rss,args -p "$PMPID" 2>/dev/null
echo ""
echo "=== 全部 postgres 主进程 ==="
ps -eo pid,ppid,etime,args 2>/dev/null | grep -i postgres | grep -v grep | head -12
echo ""
echo "=== 今天 19:0x 的日志（崩溃/重启判定）==="
grep -E '^2026-10-02 19:0' /var/lib/postgresql/data/log/postgresql-2026-10-02_000000.log 2>/dev/null | grep -vE 'in recovery mode|Consistent recovery' | head -15