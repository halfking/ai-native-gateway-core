#!/usr/bin/env bash
# scripts/ops/nginx-add-version-endpoint.sh
#
# 2026-08-31: deploy-seamless.sh step 9 verifies the public ingress returns
# the expected build version via `curl -kfsS https://127.0.0.1/version |
# grep -F "version":"<expected>"`. The gateway exposes /version as a
# JSON handler (see domains/streaming/handler.go serveVersion), but env
# 154's nginx config (llm-kxpms-cn.conf) only routes /healthz, /readyz,
# /metrics, /menu-config.json, /api/admin/live-stream, and the SPA
# catch-all — so /version falls through to the SPA fallback and returns
# index.html instead of the gateway JSON, causing the deploy to
# auto-rollback at step 9.
#
# This script adds `location = /version` to llm-kxpms-cn.conf so the
# gateway handles it. Idempotent: bails out if the location is already
# present. Run on env 154 via:
#
#   bash scripts/ops/nginx-add-version-endpoint.sh /etc/nginx/conf.d/llm-kxpms-cn.conf
#
# Why awk+counter: nginx `location = /healthz { ... }` is a multi-line
# block. Naively inserting after the opening `{` nests the new block
# inside /healthz which nginx rejects with
#   location "/version" cannot be inside the exact location "/healthz"
# We track brace depth from the `/healthz {` opener until the matching
# `}`, then insert the new block immediately after.
set -euo pipefail

CONF="${1:-/etc/nginx/conf.d/llm-kxpms-cn.conf}"

if [[ ! -f "$CONF" ]]; then
  echo "nginx config not found: $CONF" >&2
  exit 1
fi

if grep -qE 'location = /version \{' "$CONF"; then
  echo "location = /version already present in $CONF; nothing to do"
  exit 0
fi

BAK="${CONF}.bak-add-version-$(date +%Y%m%d_%H%M%S)"
cp "$CONF" "$BAK"
echo "backed up to $BAK"

awk '
  BEGIN { depth = 0; inserted = 0 }
  /location = \/healthz \{/ && !inserted {
    in_block = 1
    depth = 1
    print
    next
  }
  in_block {
    n = gsub(/\{/, "{")
    m = gsub(/\}/, "}")
    depth += n - m
    if (depth == 0) {
      print
      print ""
      print "    # 2026-08-31: deploy-seamless.sh step 9 (公网入口版本身份校验)"
      print "    # probes https://127.0.0.1/version and greps for the expected"
      print "    # build version. Without this explicit location, /version"
      print "    # falls through to the SPA fallback and returns index.html"
      print "    # instead of the gateway JSON response. Mirrors /healthz and"
      print "    # /readyz location blocks above."
      print "    location = /version {"
      print "        proxy_pass http://llm_local;"
      print "        proxy_http_version 1.1;"
      print "        proxy_set_header Host $host;"
      print "        proxy_set_header X-Real-IP $remote_addr;"
      print "        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;"
      print "        proxy_set_header X-Forwarded-Proto $scheme;"
      print "        proxy_connect_timeout 30s;"
      print "        proxy_read_timeout 30s;"
      print "        proxy_send_timeout 30s;"
      print "    }"
      in_block = 0
      inserted = 1
      next
    }
    print
    next
  }
  { print }
' "$CONF" > "$CONF.new"
mv "$CONF.new" "$CONF"

nginx -t
systemctl reload nginx
echo "---"
echo "verify:"
curl -sS -i "https://127.0.0.1/version" 2>&1 | head -10
