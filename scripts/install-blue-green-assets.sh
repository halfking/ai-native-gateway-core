#!/usr/bin/env bash
# Install blue-green assets on a target host. This script only installs files
# and validates nginx; it never starts a candidate or changes traffic.
set -euo pipefail

ROOT="${1:-/opt/llm-gateway-go}"
TARGET="${2:-154}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

case "$TARGET" in
  154)
    unit="$SCRIPT_DIR/../deploy/llm-gateway-go-canary@.service"
    nginx_fragment="$ROOT/run/active-upstream.conf"
    nginx_unit="llm-gateway-go-canary@.service"
    default_port=8781
    ;;
  245)
    unit="$SCRIPT_DIR/../deploy/llmgo-245-canary@.service"
    nginx_fragment="$ROOT/run/active-upstream.conf"
    nginx_unit="llmgo-245-canary@.service"
    default_port=8781
    ;;
  *) echo "unsupported target: $TARGET" >&2; exit 64 ;;
esac

[[ -f "$unit" ]] || { echo "missing candidate unit: $unit" >&2; exit 1; }
install -d -m 0755 "$ROOT/run" "$ROOT/slots"
install -m 0644 "$unit" "/etc/systemd/system/$nginx_unit"
if [[ ! -s "$nginx_fragment" ]]; then
  printf 'server 127.0.0.1:%s max_fails=3 fail_timeout=10s;\n' "$default_port" >"$nginx_fragment"
  chmod 0644 "$nginx_fragment"
fi
systemctl daemon-reload
if command -v nginx >/dev/null 2>&1; then
  nginx -t
fi
printf 'blue-green assets installed: target=%s unit=%s fragment=%s\n' "$TARGET" "$nginx_unit" "$nginx_fragment"
