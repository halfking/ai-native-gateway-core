#!/bin/bash
# Verify gzip is enabled for the SSE live-stream endpoint after nginx reload.
#
# Usage:
#   printf '%s' 'Bearer <admin-token>' | ./scripts/verify-gzip-sse.sh [endpoint_url]
#   ./scripts/verify-gzip-sse.sh [endpoint_url]  # prompts when stdin is a TTY
#
# The token is deliberately read from stdin rather than argv so it is not
# exposed through the process list. It is placed in a mode-0600 curl config
# file because curl -H would otherwise expose it in curl's argv too.
#
# Method: GET (not HEAD). nginx decides gzip per the real streamed response;
# a HEAD exchange has no body and is not a reliable signal for SSE.
set -uo pipefail

ENDPOINT="${1:-https://llmgo.kxpms.cn/api/admin/live-stream}"
MAX_TIME="${VERIFY_MAX_TIME:-6}"

if [ -t 0 ]; then
    printf 'Admin Authorization value (for example, Bearer <token>): ' >&2
    IFS= read -r -s ADMIN_TOKEN
    printf '\n' >&2
else
    IFS= read -r ADMIN_TOKEN || true
fi

if [ -z "${ADMIN_TOKEN:-}" ]; then
    echo "❌ Error: admin Authorization value is required" >&2
    echo "Usage: printf '%s' 'Bearer <token>' | $0 [endpoint_url]" >&2
    exit 1
fi

case "$ADMIN_TOKEN" in
    *$'\n'*|*$'\r'*)
        echo "❌ Error: Authorization value must not contain a newline" >&2
        exit 1
        ;;
esac

# Escape curl-config string delimiters. The config file is only readable by
# this user and removed at exit; do not pass the token in curl argv.
TOKEN_ESCAPED=$(printf '%s' "$ADMIN_TOKEN" | sed 's/\\/\\\\/g; s/"/\\"/g')
HEADER_FILE=$(mktemp)
FINAL_HEADER_FILE=$(mktemp)
CURL_CONFIG=$(mktemp)
META_FILE=$(mktemp)
ERROR_FILE=$(mktemp)
chmod 600 "$CURL_CONFIG"
trap 'rm -f "$HEADER_FILE" "$FINAL_HEADER_FILE" "$CURL_CONFIG" "$META_FILE" "$ERROR_FILE"' EXIT

cat >"$CURL_CONFIG" <<EOF
header = "Authorization: $TOKEN_ESCAPED"
EOF

echo "Testing gzip for $ENDPOINT ..."
echo "Authorization: provided securely via stdin"
echo ""

# curl exit 28 (timeout) is expected: a healthy SSE response remains open
# until we deliberately stop it after MAX_TIME seconds. Other curl failures
# are errors even when a proxy happened to send response headers.
set +e
curl -sS --max-time "$MAX_TIME" \
    -H "Accept-Encoding: gzip" \
    --config "$CURL_CONFIG" \
    -D "$HEADER_FILE" \
    -o /dev/null \
    -w '%{http_code}\n%{content_type}\n' \
    "$ENDPOINT" >"$META_FILE" 2>"$ERROR_FILE"
CURL_EXIT=$?
set -e

HTTP_STATUS=$(sed -n '1p' "$META_FILE")
CONTENT_TYPE=$(sed -n '2p' "$META_FILE" | tr '[:upper:]' '[:lower:]')

if [ "$CURL_EXIT" -ne 0 ] && [ "$CURL_EXIT" -ne 28 ]; then
    echo "❌ Request failed before SSE verification (curl exit $CURL_EXIT)" >&2
    sed -n '1,5p' "$ERROR_FILE" >&2
    exit 1
fi

# curl writes one header block per response (for example, redirects). Only the
# last block belongs to the response described by %{http_code}/%{content_type}.
awk 'BEGIN { block = "" } /^HTTP\/[0-9.]+ [0-9][0-9][0-9]/ { block = "" } { block = block $0 ORS } END { printf "%s", block }' \
    "$HEADER_FILE" >"$FINAL_HEADER_FILE"

if [ ! -s "$FINAL_HEADER_FILE" ] || [ -z "$HTTP_STATUS" ]; then
    echo "❌ No response headers received" >&2
    echo "Troubleshooting: endpoint URL, auth validity, network reachability" >&2
    exit 1
fi

echo "Final status: $HTTP_STATUS"
echo "Final content type: ${CONTENT_TYPE:-<missing>}"
echo "Final response headers:"
sed -n '1,15p' "$FINAL_HEADER_FILE"
echo ""

case "$HTTP_STATUS" in
    2??) ;;
    *)
        echo "❌ Endpoint did not establish a successful SSE response (HTTP $HTTP_STATUS)" >&2
        exit 1
        ;;
esac

case "$CONTENT_TYPE" in
    text/event-stream* ) ;;
    *)
        echo "❌ Endpoint returned a non-SSE response; refusing to treat gzip as verified" >&2
        exit 1
        ;;
esac

if grep -qi '^content-encoding:[[:space:]]*gzip[[:space:]]*$' "$FINAL_HEADER_FILE"; then
    echo "✅ gzip enabled for a successful SSE response"
    exit 0
fi

echo "❌ gzip NOT enabled for the SSE response" >&2
echo ""
echo "Troubleshooting:" >&2
echo "1. Verify nginx config syntax: nginx -t" >&2
echo "2. Reload nginx: systemctl reload nginx" >&2
echo "3. Check the server's nginx error log (path varies per vhost)" >&2
echo "4. Verify gzip directives are in server {} block, not location {}" >&2
echo "5. Confirm no 'gzip off' inside the /api/admin/live-stream location" >&2
exit 1
