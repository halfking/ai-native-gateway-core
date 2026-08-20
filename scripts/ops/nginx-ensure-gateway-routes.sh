#!/usr/bin/env bash
# Ensure a Gateway vhost does not send backend routes to an SPA fallback.
set -euo pipefail

CONF="${NGINX_CONF:?NGINX_CONF is required}"
UPSTREAM="${NGINX_GATEWAY_UPSTREAM:?NGINX_GATEWAY_UPSTREAM is required}"

[[ -f "$CONF" ]] || { echo "ensure gateway routes failed: config not found (path=$CONF)" >&2; exit 1; }

if grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/api/[[:space:]]*\{' "$CONF" \
  && grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/v1/[[:space:]]*\{' "$CONF"; then
  echo "Gateway route boundaries already configured (path=$CONF)"
  exit 0
fi

backup="${CONF}.bak-gateway-routes-$(date +%Y%m%d_%H%M%S)"
cp "$CONF" "$backup"

python3 - "$CONF" "$UPSTREAM" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
upstream = sys.argv[2]
text = path.read_text(encoding="utf-8")
marker = "    location / {"
if text.count(marker) != 1:
    raise SystemExit(f"ensure gateway routes failed: expected one SPA location marker (path={path})")

block = f'''    location = /healthz {{
        proxy_pass http://{upstream};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 30s;
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
    }}
    location = /readyz {{
        proxy_pass http://{upstream};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 30s;
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
    }}
    location ^~ /api/ {{
        proxy_pass http://{upstream};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_read_timeout 1200s;
        proxy_send_timeout 1200s;
        proxy_buffering off;
        proxy_cache off;
    }}
    location ^~ /v1/ {{ proxy_pass http://{upstream}; }}
    location ^~ /v1beta/ {{ proxy_pass http://{upstream}; }}
    location ^~ /v2/ {{ proxy_pass http://{upstream}; }}
    location = /metrics {{ proxy_pass http://{upstream}; }}
    location ^~ /admin/ {{ proxy_pass http://{upstream}; }}

path.write_text(text.replace(marker, block + marker, 1), encoding="utf-8")
PY

nginx -t
systemctl reload nginx
echo "Gateway route boundaries enabled; backup=$backup"
