#!/bin/bash
# Verify gzip is enabled for the SSE live-stream endpoint after nginx reload.
#
# Usage: ./scripts/verify-gzip-sse.sh [endpoint_url] [admin_token]
# Example:
#   ./scripts/verify-gzip-sse.sh https://llmgo.kxpms.cn/api/admin/live-stream "Bearer xxx"
#
# Method: GET (not HEAD). nginx decides gzip per the real streamed response;
# a HEAD exchange has no body and is not a reliable signal for SSE.
# The stream is read for a few seconds, then headers are inspected.
set -uo pipefail

ENDPOINT="${1:-https://llmgo.kxpms.cn/api/admin/live-stream}"
ADMIN_TOKEN="${2:-}"
MAX_TIME="${VERIFY_MAX_TIME:-6}"

if [ -z "$ADMIN_TOKEN" ]; then
    echo "❌ Error: Admin token required"
    echo "Usage: $0 <endpoint_url> <admin_token>"
    echo "Example: $0 https://llmgo.kxpms.cn/api/admin/live-stream 'Bearer xxx'"
    exit 1
fi

# Mask the token: scheme + first 6 chars only, never the full secret.
SCHEME="${ADMIN_TOKEN%% *}"
REST="${ADMIN_TOKEN#* }"
if [ "$REST" != "$ADMIN_TOKEN" ]; then
    TOKEN_MASKED="$SCHEME $(printf '%s' "$REST" | cut -c1-6)…"
else
    TOKEN_MASKED="$(printf '%s' "$ADMIN_TOKEN" | cut -c1-6)…"
fi

echo "Testing gzip for $ENDPOINT ..."
echo "Authorization: $TOKEN_MASKED (masked)"
echo ""

HEADER_FILE=$(mktemp)
trap 'rm -f "$HEADER_FILE"' EXIT

# curl exit 28 (timed out) is EXPECTED for a healthy SSE stream: we stop
# reading after MAX_TIME seconds on purpose. The check below only needs the
# response headers captured via -D.
curl -sS --max-time "$MAX_TIME" \
    -H "Accept-Encoding: gzip" \
    -H "Authorization: $ADMIN_TOKEN" \
    -D "$HEADER_FILE" \
    -o /dev/null \
    "$ENDPOINT" || true

if [ ! -s "$HEADER_FILE" ]; then
    echo "❌ No response headers received"
    echo "Troubleshooting: endpoint URL, auth token validity, network reachability"
    exit 1
fi

echo "Response headers:"
sed -n '1,15p' "$HEADER_FILE"
echo ""

if grep -qi "^content-encoding: gzip" "$HEADER_FILE"; then
    echo "✅ gzip enabled - SSE responses will be compressed"
    exit 0
else
    echo "❌ gzip NOT enabled - check nginx config and reload status"
    echo ""
    echo "Troubleshooting:"
    echo "1. Verify nginx config syntax: nginx -t"
    echo "2. Reload nginx: systemctl reload nginx"
    echo "3. Check the server's nginx error log (path varies per vhost)"
    echo "4. Verify gzip directives are in server {} block, not location {}"
    echo "5. Confirm no 'gzip off' inside the /api/admin/live-stream location"
    exit 1
fi
