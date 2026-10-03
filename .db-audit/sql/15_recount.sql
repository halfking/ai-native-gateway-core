export PGPAGER=cat
export TERM=dumb
P="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off"
$P -c "SELECT 'public_r_or_p='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p');"
$P -c "SELECT 'public_partition_leaves='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_inherits i ON i.inhrelid=c.oid WHERE n.nspname='public' AND c.relkind='r';"
$P -c "SELECT 'public_base_tables='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid);"
$P -c "SELECT 'public_indexes='||count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='i';"