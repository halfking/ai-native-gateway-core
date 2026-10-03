pg_dump -U llm_gateway -d llm_gateway --schema-only --no-owner --no-acl --quote-all-identifiers -f /tmp/34_schema.sql 2>/tmp/34_dump.err
echo "DUMPERR_BEGIN"
cat /tmp/34_dump.err
echo "DUMPERR_END"
echo "RAWBYTES=$(wc -c < /tmp/34_schema.sql)"
echo "GZBYTES=$(gzip -9 -c /tmp/34_schema.sql | wc -c)"
echo "SHA=$(sha256sum /tmp/34_schema.sql | cut -d' ' -f1)"
echo "B64_BEGIN"
gzip -9 -c /tmp/34_schema.sql | base64 -w 0
echo ""
echo "B64_END"
