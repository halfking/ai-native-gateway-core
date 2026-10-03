export PGPAGER=cat
export TERM=dumb
echo "=== 容器内我的 exec 残留进程 ==="
ps -eo pid,ppid,etime,rss,args 2>/dev/null | grep -E 'psql|sh -c' | grep -v grep | head -20
echo ""
echo "=== 磁盘占用 TOP ==="
du -sh /var/lib/postgresql/data/pg_wal 2>/dev/null
du -sh /var/lib/postgresql/data/base 2>/dev/null
echo ""
echo "=== WAL 段数量 ==="
ls -1 /var/lib/postgresql/data/pg_wal 2>/dev/null | wc -l
echo ""
echo "=== 是否有未回收的 COPY/列存临时文件 ==="
find /var/lib/postgresql/data/base -name 'cidt_*' -newermt '-2 hours' 2>/dev/null | head -5
find /var/lib/postgresql/data -maxdepth 2 -name '*audit_scratch*' 2>/dev/null | head -5
echo ""
echo "=== PG 后端（谁在占资源）==="
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT pid||' | '||datname||' | '||state||' | '||coalesce(wait_event_type,'-')||' | backend_start||' | '||left(regexp_replace(query,E'[\n\r\t ]+',' ','g'),80) FROM pg_stat_activity ORDER BY backend_start LIMIT 20;"
echo ""
echo "=== checkpoint 状态（确认是不是 WAL 涨的）==="
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'timed='||num_timed||' requested='||num_requested||' write_time_ms='||write_time||' buffers_written='||buffers_written FROM pg_stat_checkpointer;"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'wal_bytes_4d='||pg_size_pretty(sum(wal_bytes)) FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname='llm_gateway');"
