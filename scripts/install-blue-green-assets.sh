#!/usr/bin/env bash
# Install blue-green assets on a target host. This script only installs files
# and validates nginx; it never starts a candidate or changes traffic.
#
# 2026-08-31: env 154/245 historically carried a slot-based canary unit
# (ExecStart=/opt/llm-gateway-go/slots/%i/llm-gateway-go, Restart=no) that the
# seamless deployer never wired up — slots/ stayed empty, candidates failed
# to bind, and the deploy script could not pinpoint the cause. We now ship a
# single contract (ExecStart follows the active binary symlink,
# Restart=on-failure) and migrate any pre-existing units on the target. The
# migration is one-shot: it backs up the old unit to
# /etc/systemd/system/<unit>.pre-blue-green-assets-YYYYMMDD-HHMMSS and
# installs the new one. Existing instances are not restarted automatically;
# operators run the next deploy (which restarts canary instances).
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

# Detect legacy unit (slot-based ExecStart OR an unrelated prior install) and
# back it up before overwriting. We deliberately do NOT restart the canary
# here — install-blue-green-assets is a file-only installer. The next deploy
# invocation picks up the new unit.
# Production targets put the unit under /etc/systemd/system/ regardless of ROOT.
# The offline test harness pins ROOT to a sandbox and uses HOST_INSTALL_ROOT
# to redirect everything (including systemd paths) under that sandbox.
if [[ -n "${HOST_INSTALL_ROOT:-}" ]]; then
  target_unit="$ROOT/etc/systemd/system/${nginx_unit}"
else
  target_unit="/etc/systemd/system/${nginx_unit}"
fi
if [[ -f "$target_unit" ]] && ! diff -q "$target_unit" "$unit" >/dev/null 2>&1; then
  ts=$(date -u +%Y%m%d-%H%M%S)
  backup="${target_unit}.pre-blue-green-assets-${ts}"
  cp -p "$target_unit" "$backup"
  echo "blue-green-assets: existing unit differs from canonical; backed up to $backup"
fi

install -d -m 0755 "$ROOT/run" "$ROOT/slots"
# Production targets write the unit directly into /etc/systemd/system/, which
# already exists. The offline test harness (HOST_INSTALL_ROOT) writes the
# unit under $ROOT/etc/systemd/system/ and we must mkdir that path first.
if [[ -n "${HOST_INSTALL_ROOT:-}" ]]; then
  install -d -m 0755 "$(dirname "$target_unit")"
fi
install -m 0644 "$unit" "$target_unit"
if [[ ! -s "$nginx_fragment" ]]; then
  printf 'server 127.0.0.1:%s max_fails=3 fail_timeout=10s;\n' "$default_port" >"$nginx_fragment"
  chmod 0644 "$nginx_fragment"
fi
# No canary.env is written anymore. The per-port identity (listen address,
# log file) lives on the unit's ExecStart via /usr/bin/env where systemd's %i
# instance specifier expands; systemd EnvironmentFile does not support
# specifier expansion, and the 2026-08-31 incident was caused exactly by a
# hard-coded listen port in a canary.env file (the candidate port alternates
# per deploy, so a frozen port made every second deploy fight the active for
# the same port and die on "bind: address already in use"). The runtime role
# is a plain Environment= directive in the unit. A stale canary.env left by
# an older install is inert: no unit references it.
systemctl daemon-reload
if command -v nginx >/dev/null 2>&1; then
  nginx -t
fi
printf 'blue-green assets installed: target=%s unit=%s fragment=%s\n' "$TARGET" "$nginx_unit" "$nginx_fragment"
