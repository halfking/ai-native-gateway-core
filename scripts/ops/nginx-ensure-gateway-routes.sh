#!/usr/bin/env bash
# Ensure a Gateway vhost does not send backend routes to an SPA fallback.
set -euo pipefail

CONF="${NGINX_CONF:?NGINX_CONF is required}"
UPSTREAM="${NGINX_GATEWAY_UPSTREAM:?NGINX_GATEWAY_UPSTREAM is required}"
NGINX_BIN="${NGINX_BIN:-nginx}"
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"

[[ -f "$CONF" ]] || { echo "ensure gateway routes failed: config not found (path=$CONF)" >&2; exit 1; }

if grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/api/[[:space:]]*\{' "$CONF" \
  && grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/v1/[[:space:]]*\{' "$CONF"; then
  echo "Gateway route boundaries already configured (path=$CONF)"
  exit 0
fi

backup="${CONF}.bak-gateway-routes-$(date +%Y%m%d_%H%M%S)"
tmp="${CONF}.tmp-gateway-routes-$$"
cleanup() { rm -f "$tmp"; }
trap cleanup EXIT

python3 - "$CONF" "$tmp" "$UPSTREAM" <<'PY'
from pathlib import Path
import re
import sys

source = Path(sys.argv[1])
target = Path(sys.argv[2])
upstream = sys.argv[3]
text = source.read_text(encoding="utf-8")


def matching_brace(value: str, opening: int) -> int:
    depth = 0
    quote = None
    escaped = False
    for pos in range(opening, len(value)):
        char = value[pos]
        if quote:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            continue
        if char in "'\"":
            quote = char
        elif char == "{":
            depth += 1
        elif char == "}":
            depth -= 1
            if depth == 0:
                return pos
    raise SystemExit("ensure gateway routes failed: unterminated nginx block")


server_blocks = []
for match in re.finditer(r"\bserver\s*\{", text):
    end = matching_brace(text, match.end() - 1)
    server_blocks.append((match.start(), end, text[match.start():end + 1]))

candidates = []
for start, end, block in server_blocks:
    if not re.search(r"\blisten\s+443\b[^;]*\bssl\b", block):
        continue
    for location in re.finditer(r"^[ \t]*location\s+/\s*\{", block, re.MULTILINE):
        candidates.append(start + location.start())

if len(candidates) != 1:
    raise SystemExit(
        "ensure gateway routes failed: expected one HTTPS SPA location, "
        f"found {len(candidates)} (path={source})"
    )

proxy_common = f"""        proxy_pass http://{upstream};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
"""
backend = f"""    location = /healthz {{
{proxy_common}        proxy_connect_timeout 30s;
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
    }}
    location = /readyz {{
{proxy_common}        proxy_connect_timeout 30s;
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
    }}
    location ^~ /api/ {{
{proxy_common}        proxy_connect_timeout 60s;
        proxy_read_timeout 1200s;
        proxy_send_timeout 1200s;
        proxy_buffering off;
        proxy_cache off;
    }}
    location ^~ /v1/ {{
{proxy_common}        proxy_connect_timeout 60s;
        proxy_read_timeout 3600s;
        proxy_send_timeout 1200s;
        proxy_buffering off;
        proxy_cache off;
    }}
    location ^~ /v1beta/ {{
{proxy_common}        proxy_connect_timeout 60s;
        proxy_read_timeout 1200s;
        proxy_send_timeout 1200s;
        proxy_buffering off;
        proxy_cache off;
    }}
    location ^~ /v2/ {{
{proxy_common}        proxy_connect_timeout 60s;
        proxy_read_timeout 3600s;
        proxy_send_timeout 1200s;
        proxy_buffering off;
        proxy_cache off;
    }}
    location = /metrics {{
{proxy_common}        proxy_connect_timeout 30s;
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
    }}
    location ^~ /admin/ {{
{proxy_common}        proxy_connect_timeout 30s;
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;
    }}

"""
insert_at = candidates[0]
target.write_text(text[:insert_at] + backend + text[insert_at:], encoding="utf-8")
PY

cp "$CONF" "$backup"
cp "$tmp" "$CONF"
if ! "$NGINX_BIN" -t; then
  cp "$backup" "$CONF"
  echo "ensure gateway routes failed: nginx validation failed; restored $CONF" >&2
  exit 1
fi
if ! "$SYSTEMCTL_BIN" reload nginx; then
  cp "$backup" "$CONF"
  "$NGINX_BIN" -t >/dev/null
  echo "ensure gateway routes failed: reload failed; restored $CONF" >&2
  exit 1
fi
echo "Gateway route boundaries enabled; backup=$backup"
