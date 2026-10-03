export PGPAGER=cat
export TERM=dumb
# Clean up orphaned docker-exec sessions left behind by earlier audit runs.
# Match only audit-originated shells and their psql/pager children; the Postgres
# server processes and the llm-gateway app connections are untouched.
echo "=== before ==="
ps -eo pid,etime,args 2>/dev/null | grep -E 'sh -c -- pager|psql -U llm_gateway' | grep -v grep | awk '{print $1, $2}' | wc -l

for p in $(ps -eo pid,args 2>/dev/null | grep 'sh -c -- pager' | grep -v grep | awk '{print $1}'); do
  kill -9 "$p" 2>/dev/null && echo "killed pager sh $p"
done

for p in $(ps -eo pid,args 2>/dev/null | grep -E 'psql -U llm_gateway' | grep -v grep | awk '{print $1}'); do
  kill -9 "$p" 2>/dev/null && echo "killed psql $p"
done

for p in $(ps -eo pid,args 2>/dev/null | grep -E '/bin/sh -c' | grep -v grep | grep -E 'audit_scratch|bak tables remain|columnar_tables_before|SELECT version|STEP1|DB SIZES' | awk '{print $1}'); do
  kill -9 "$p" 2>/dev/null && echo "killed exec sh $p"
done

sleep 2
echo ""
echo "=== after ==="
ps -eo pid,etime,args 2>/dev/null | grep -E 'sh -c -- pager|psql -U llm_gateway' | grep -v grep | wc -l
echo ""
echo "=== postgres backends now ==="
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT state||' = '||count(*) FROM pg_stat_activity WHERE datname='llm_gateway' GROUP BY state;"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'total backends = '||count(*) FROM pg_stat_activity;"
df -h /var/lib/postgresql/data | tail -1
