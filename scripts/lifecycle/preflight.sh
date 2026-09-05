#!/usr/bin/env bash
# scripts/lifecycle/preflight.sh — llm-gateway-go 启动前自检
#
# 三段硬性检查（任何一段失败即拒绝启动）：
#   1. /healthz  — 进程在跑
#   2. /readyz   — 自检完成，能对外服务
#   3. /version  — 解析出的版本必须等于 EXPECTED_VERSION（去掉 v 前缀，大小写不敏感）
#                  防止"切完只查 healthz，起一个会回 200 的旧二进制也被判定为成功"
#
# 选段检查：
#   --check-db  用 LLM_GATEWAY_DATABASE_URL 探一次 SELECT 1，确认 DB 可达且 schema 已迁
#
# Usage:
#   preflight.sh --base-url http://127.0.0.1:8080 [--expected-version 2.4.7.1795] [--check-db]
#   preflight.sh --release-dir /opt/llm-gateway/releases/2.4.7.1795 --expected-version 2.4.7.1795
#
# Exit codes:
#   0 = all passed; non-zero = first failed segment.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

base_url=""
expected_version=""
check_db=0
release_dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-url)         base_url=${2:-}; shift 2 ;;
    --expected-version) expected_version=${2:-}; shift 2 ;;
    --release-dir)      release_dir=${2:-}; shift 2 ;;
    --check-db)         check_db=1; shift ;;
    -h|--help)
      sed -n '2,21p' "${BASH_SOURCE[0]}"
      exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

# 默认 base_url 从 HEALTH_URL env 推断；fallback 127.0.0.1:8080。
if [[ -z "$base_url" ]]; then
  base_url="${HEALTH_URL:-http://127.0.0.1:8080}"
fi

# /version 端点 host（与 base_url 同源；如果 base_url 走反代 nginx，要单独配 VERSION_URL）。
version_url="${VERSION_URL:-${base_url%/healthz}/version}"
readyz_url="${READYZ_URL:-${base_url%/healthz}/readyz}"
healthz_url="${base_url%/healthz}/healthz"

# 如果传了 --release-dir，从 version.json 读出 expected version。
if [[ -z "$expected_version" && -n "$release_dir" ]]; then
  expected_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$release_dir/version.json" 2>/dev/null || true)"
fi

failures=0

# ─── Stage 1: /healthz ─────────────────────────────────────────────────────
if curl -fsS --max-time 5 "$healthz_url" >/dev/null 2>&1; then
  log "preflight healthz OK ($healthz_url)"
else
  warn "preflight healthz FAILED ($healthz_url)"
  failures=$((failures+1))
fi

# ─── Stage 2: /readyz ──────────────────────────────────────────────────────
if curl -fsS --max-time 5 "$readyz_url" >/dev/null 2>&1; then
  log "preflight readyz OK ($readyz_url)"
else
  warn "preflight readyz FAILED ($readyz_url)"
  failures=$((failures+1))
fi

# ─── Stage 3: /version (only when expected_version set) ────────────────────
if [[ -n "$expected_version" ]]; then
  body="$(curl -fsS --max-time 5 "$version_url" 2>/dev/null || true)"
  if [[ -z "$body" ]]; then
    warn "preflight version: empty/non-2xx from $version_url"
    failures=$((failures+1))
  else
    got="$(printf '%s' "$body" | python3 -c '
import json, sys
try:
    d = json.loads(sys.stdin.read() or "{}")
except Exception:
    raise SystemExit(0)
for k in ("version", "service_version", "build_version"):
    v = d.get(k)
    if isinstance(v, str) and v:
        print(v)
        raise SystemExit(0)
')"
    if [[ -z "$got" ]]; then
      warn "preflight version: no version field in $version_url response"
      failures=$((failures+1))
    else
      want_have="${expected_version#v}"
      got_have="${got#v}"
      if [[ "${got_have,,}" != "${want_have,,}" ]]; then
        warn "preflight version: reported=$got expected=$want_have"
        failures=$((failures+1))
      else
        log "preflight version OK ($got matches expected=$want_have)"
      fi
    fi
  fi
fi

# ─── Optional: DB SELECT 1 ──────────────────────────────────────────────────
if [[ "$check_db" == "1" ]]; then
  require_value LLM_GATEWAY_DATABASE_URL "${LLM_GATEWAY_DATABASE_URL:-}"
  if psql "$LLM_GATEWAY_DATABASE_URL" -X -v ON_ERROR_STOP=1 -Atqc 'SELECT 1' 2>/dev/null | grep -qx '1'; then
    log "preflight db SELECT 1 OK"
  else
    warn "preflight db SELECT 1 FAILED"
    failures=$((failures+1))
  fi
fi

if [[ "$failures" -gt 0 ]]; then
  die "preflight FAILED ($failures stage(s))"
fi
log "preflight checks passed"
