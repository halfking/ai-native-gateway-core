export PGPAGER=cat
export TERM=dumb
P="psql -U llm_gateway -d llm_gateway -A -F| -t -P pager=off -v ON_ERROR_STOP=1"
# Authoritative public-schema column inventory: base tables + partitioned parents,
# partitions excluded so the shape matches the repo bootstrap baseline.
$P -c "SELECT c.relname||'|'||a.attname||'|'||format_type(a.atttypid,a.atttypmod) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname='public' AND c.relkind IN ('r','p') AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid) ORDER BY c.relname, a.attname;"
echo "===PARTITIONED_PARENT_MARKER==="
$P -c "SELECT c.relname||'|'||pg_get_partkeydef(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='p' ORDER BY 1;"
echo "===INDEX_INVENTORY==="
$P -c "SELECT i.relname||'|'||t.relname||'|'||pg_get_indexdef(i.oid) FROM pg_class i JOIN pg_index x ON x.indexrelid=i.oid JOIN pg_class t ON t.oid=x.indrelid JOIN pg_namespace n ON n.oid=i.relnamespace WHERE n.nspname='public' ORDER BY t.relname, i.relname;"
