pg_dump -U llm_gateway -d llm_gateway --schema-only --no-owner --no-acl --quote-all-identifiers -f /tmp/34_schema.sql 2>/tmp/34_dump.err
echo "exit=$?"
ls -l /tmp/34_schema.sql
cat /tmp/34_dump.err
cat /tmp/34_schema.sql
