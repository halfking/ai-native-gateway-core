export PGPAGER=cat
export TERM=dumb
export PGCLIENTOPTIONS='--statement_timeout=30s --lock_timeout=5s'
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'uptime='||(now()-pg_postmaster_start_time())::text||' | recovery_ready=yes';"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'shared_buffers='||current_setting('shared_buffers')||' | work_mem='||current_setting('work_mem');"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'app_conns='||count(*) FROM pg_stat_activity WHERE datname='llm_gateway' AND application_name<>'psql';"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'longest_query='||coalesce(max(extract(epoch from now()-query_start))::int,0)||'s';"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'databases='||count(*) FROM pg_database WHERE datallowconn;"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'bak_tables='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname LIKE 'bak\_%';"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'scratch_residue='||(to_regnamespace('audit_scratch') IS NOT NULL)::text||' | columnar_tables='||(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='c');"
df -h /var/lib/postgresql/data | tail -1