#!/bin/bash
# Verify gzip is enabled for SSE endpoint after nginx reload
# Usage: ./scripts/verify-gzip-sse.sh [endpoint_url] [admin_token]
# Example: ./scripts/verify-gzip-sse.sh https://llmgo.kxpms.cn/api/admin/live-stream "Bearer xxx"

set -euo pipefail

ENDPOINT="${1:-https://llmgo.kxpms.cn/api/admin/live-stream}"
ADMIN_TOKEN="${2:-}"

if [ -z "$ADMIN_TOKEN" ]; then
    echo "❌ Error: Admin token required"
    echo "Usage: $0 <endpoint_url> <admin_token>"
    echo "Example: $0 https://llmgo.kxpms.cn/api/admin/live-stream 'Bearer xxx'"
    exit 1
fi

echo "Testing gzip for $ENDPOINT ..."
echo "Authorization: $ADMIN_TOKEN (masked)"
echo ""

# Fetch headers with gzip encoding request
RESPONSE=$(curl -sI \
    -H "Accept-Encoding: gzip" \
    -H "Authorization: $ADMIN_TOKEN" \
    "$ENDPOINT" 2>&1 | head -20)

echo "Response headers (first 20 lines):"
echo "$RESPONSE"
echo ""

# Check for gzip encoding
if echo "$RESPONSE" | grep -qi "Content-Encoding: gzip"; then
    echo "✅ gzip enabled - SSE responses will be compressed"
    exit 0
else
    echo "❌ gzip NOT enabled - check nginx config and reload status"
    echo ""
    echo "Troubleshooting:"
    echo "1. Verify nginx config syntax: nginx -t"
    echo "2. Reload nginx: systemctl reload nginx"
    echo "3. Check nginx error log: tail -n 50 /var/log/nginx/llmgo.kxpms.cn-error.log"
    echo "4. Verify gzip directives are in server {} block, not location {}"
    exit 1
fi
