export PGPAGER=cat
export TERM=dumb
export PGCLIENTOPTIONS='--statement_timeout=30s'
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'current_setting = '||current_setting('shared_buffers');"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'pg_settings     = '||setting||coalesce(unit,'')||'  source='||source||'  boot_val='||boot_val||coalesce(unit,'');"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'in bytes       = '||(SELECT setting::bigint*8192 FROM pg_settings WHERE name='shared_buffers');"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'SHOW shared_buffers = '||current_setting('shared_buffers', true);"
psql -U llm_gateway -d llm_gateway -A -F'|' -t -P pager=off -c "SELECT 'config_file = '||current_setting('config_file');"