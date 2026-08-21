#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# File:          scripts/ops/nginx-ensure-gateway-routes.sh
# Purpose:       Ensure a Gateway vhost does not send backend routes to an
#                SPA fallback. Idempotent: safe to re-run.
#
# Status:        active
# Idempotent:    YES
# Changelog:
#   2026-08-21  v1.0  Initial version
#                   - HTTP server block detection (listen 443 ssl)
#                   - Backup, nginx -t, reload with rollback
#                   - Pre/post HTTP sanity probes
#   2026-08-21  v1.1  Add upstream reachability pre-check
# ---------------------------------------------------------------------------
# Usage:
#   NGINX_CONF=/etc/nginx/conf.d/llmgo.kxpms.cn.conf \
#   NGINX_GATEWAY_UPSTREAM=llmgo_local_245 \
#   bash scripts/ops/nginx-ensure-gateway-routes.sh
#
# Optional:
#   PROBE_BASE_URL="https://llmgo.kxpms.cn"
#   PROBE_COOKIE_JAR="/tmp/llmgo.jar"
# ---------------------------------------------------------------------------
set -euo pipefail

CONF="${NGINX_CONF:?NGINX_CONF is required}"
UPSTREAM="${NGINX_GATEWAY_UPSTREAM:?NGINX_GATEWAY_UPSTREAM is required}"
NGINX_BIN="${NGINX_BIN:-nginx}"
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"
CURL_BIN="${CURL_BIN:-curl}"
PROBE_BASE_URL="${PROBE_BASE_URL:-}"

[[ -f "$CONF" ]] || { echo "ensure gateway routes failed: config not found (path=$CONF)" >&2; exit 1; }

# Pre-check: upstream reachability. Extract host:port from upstream name if it
# looks like host:port; otherwise assume a name resolvable in nginx config.
if [[ "$UPSTREAM" == *:* ]]; then
  up_host="${UPSTREAM%:*}"
  up_port="${UPSTREAM##*:}"
  if ! bash -c "</dev/tcp/${up_host}/${up_port}" 2>/dev/null; then
    echo "ensure gateway routes failed: upstream ${up_host}:${up_port} unreachable" >&2
    exit 1
  fi
fi

if [[ -n "${UPSTREAM_PROBE_HOST:-}" && -n "${UPSTREAM_PROBE_PORT:-}" ]]; then
  if ! bash -c "exec 3<> /dev/tcp/${UPSTREAM_PROBE_HOST}/${UPSTREAM_PROBE_PORT}" 2>/dev/null; then
    echo "ensure gateway routes failed: upstream unreachable (${UPSTREAM_PROBE_HOST}:${UPSTREAM_PROBE_PORT})" >&2
    exit 1
  fi
  exec 3>&-
  echo "Upstream reachable: ${UPSTREAM_PROBE_HOST}:${UPSTREAM_PROBE_PORT}"
fi


# Pre-check 1: upstream reachability
upstream_addr="$(awk -v u="$UPSTREAM" '
    BEGIN { found = 0 }
    $0 ~ "^[[:space:]]*upstream[[:space:]]+" u "[[:space:]]*\\{" { found = 1; next }
    found && /server[[:space:]]/ { print; exit }
' /etc/nginx/nginx.conf "$CONF" 2>/dev/null || true)"
if [[ -n "$upstream_addr" ]]; then
  host_port="$(printf '%s\n' "$upstream_addr" | awk '{for (i=1;i<=NF;i++) if ($i=="server") print $(i+1)}' | head -n1 | cut -d: -f1-2)"
  if [[ "$host_port" =~ ^([0-9A-Za-z._-]+):([0-9]+)$ ]]; then
    host="${BASH_REMATCH[1]}"
    port="${BASH_REMATCH[2]}"
    if ! "$CURL_BIN" --max-time 3 -sS -o /dev/null "http://${host}:${port}/healthz"; then
      echo "ensure gateway routes failed: upstream $host:$port/healthz unreachable" >&2
      exit 1
    fi
  fi
fi

probe_paths=(/healthz /readyz /api/auth/token /v1/models /metrics)
probe_unexpected=""
if [[ -n "$PROBE_BASE_URL" ]]; then
  for path in "${probe_paths[@]}"; do
    code_ct="$("$CURL_BIN" -skS -o /dev/null -w '%{http_code} %{content_type}' \
        -X POST -H 'Content-Type: application/json' \
        --data '{"username":"invalid","password":"invalid"}' \
        "${PROBE_BASE_URL}${path}" 2>/dev/null || echo '000 unknown')"
    if [[ "$code_ct" == *text/html* ]]; then
      probe_unexpected+="${PROBE_BASE_URL}${path} -> ${code_ct}\n"
    fi
  done
fi
if [[ -n "$probe_unexpected" ]]; then
  echo "ensure gateway routes notice: some probes still return text/html before fix:"
  printf '%b' "$probe_unexpected"
fi

if grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/api/[[:space:]]*\{' "$CONF" \
  && grep -Eq '^[[:space:]]*location[[:space:]]+\^~[[:space:]]+/v1/[[:space:]]*\{' "$CONF"; then
  echo "Gateway route boundaries already configured (path=$CONF)"
  configured=true
else
  configured=false
fi

if [[ "$configured" == false ]]; then
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
    # Vue SPA lives under /admin/* — only config hot-reload is a Go endpoint.
    # Never use location ^~ /admin/ (causes SPA 401 missing_key).
    location = /admin/config/reload {{
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

fi

if [[ -n "${PROBE_BASE_URL:-}" ]]; then
  echo "Running post-reload HTTP probes (base=$PROBE_BASE_URL)..."
  declare -a probes=(
    "GET|${PROBE_BASE_URL}/healthz|200"
    "OPTIONS|${PROBE_BASE_URL}/api/auth/token|204"
    "POST|${PROBE_BASE_URL}/api/auth/token|401"
    "GET|${PROBE_BASE_URL}/v1/models|401"
    "GET|${PROBE_BASE_URL}/metrics|401"
  )
  for entry in "${probes[@]}"; do
    IFS='|' read -r method url expected <<<"$entry"
    if [[ "$method" == "POST" ]]; then
      code=$(curl -skS -o /dev/null -w '%{http_code}' \
        -X POST -H 'Content-Type: application/json' \
        --data '{"username":"invalid","password":"invalid"}' "$url" || true)
    elif [[ "$method" == "OPTIONS" ]]; then
      code=$(curl -skS -o /dev/null -w '%{http_code}' \
        -X OPTIONS -H 'Origin: '"$PROBE_BASE_URL" \
        -H 'Access-Control-Request-Method: POST' "$url" || true)
    else
      code=$(curl -skS -o /dev/null -w '%{http_code}' "$url" || true)
    fi
    if [[ "$code" != "$expected" ]]; then
      echo "  WARN probe=$method $url expected=$expected actual=$code"
    else
      echo "  OK   probe=$method $url code=$code"
    fi
  done
fi
