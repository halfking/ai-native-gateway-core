export PGPAGER=cat
export TERM=dumb
echo "=== 容器日志尾部（崩溃原因）==="
for f in /var/lib/postgresql/data/log/*.log /var/lib/postgresql/data/*.log /var/log/postgresql/*.log; do
  [ -f "$f" ] && echo "--- $f ---" && tail -60 "$f"
done
echo ""
echo "=== docker stdout/stderr ==="
ls -la /var/lib/postgresql/data/log 2>/dev/null | head
echo ""
echo "=== 当前是否恢复完成 ==="
for i in 1 2 3 4 5 6 7 8 9 10; do
  if psql -U llm_gateway -d llm_gateway -A -t -P pager=off -c "SELECT 'READY'" 2>/dev/null | grep -q READY; then
    echo "recovery finished after ${i} probe(s)"
    psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'server_version='||current_setting('server_version');"
    psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'db_size='||pg_size_pretty(pg_database_size('llm_gateway'));"
    psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'key tables: request_logs='||(SELECT count(*) FROM request_logs)||' sessions='||(SELECT count(*) FROM sessions)||' ursm='||(SELECT count(*) FROM ursm_node_snapshot_min);"
    psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'bak_tables='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname LIKE 'bak\_%';"
    psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'audit_scratch exists='||(to_regnamespace('audit_scratch') IS NOT NULL)::text;"
    psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'recovery_done='||count(*)||' last_wal='||coalesce(max(lsn),'-') FROM pg_stat_database WHERE datname='llm_gateway';"
    break
  fi
  sleep 10
done
