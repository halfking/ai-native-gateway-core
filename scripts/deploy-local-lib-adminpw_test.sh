#!/usr/bin/env bash
# Tests for dl_admin_password_preflight (deploy-local-lib.sh).
#
# Background (12h 修订审计轮 2026-09-26，handoff §6.1 预防建议落地):
# .env.local 重建丢 LLM_GATEWAY_ADMIN_PASSWORD（09-19）时，
# sync_admin_password_from_env 只 warn+skip，凭据解密冒烟等到 ~2 分钟构建后
# 才 401。preflight 必须在「用户名已设而密码为空」形态下返回 1，其余形态
# 不设新门槛：两值同空维持旧行为，逃生口 DL_ALLOW_EMPTY_ADMIN_PASSWORD=true。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

run_case() {
  # $1=admin_user $2=admin_password $3=allow_bypass(0/1)
  bash -c '
    set -euo pipefail
    source "'"$LIB"'"
    [[ -n "'"$1"'" ]] && export LLM_GATEWAY_ADMIN_USER="'"$1"'" || unset LLM_GATEWAY_ADMIN_USER
    [[ -n "'"$2"'" ]] && export LLM_GATEWAY_ADMIN_PASSWORD="'"$2"'" || unset LLM_GATEWAY_ADMIN_PASSWORD
    [[ "'"$3"'" == 1 ]] && export DL_ALLOW_EMPTY_ADMIN_PASSWORD=true || unset DL_ALLOW_EMPTY_ADMIN_PASSWORD
    dl_admin_password_preflight
    echo PREFLIGHT_PASS
  '
}

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# 1. 用户名已设 + 密码为空（09-26 401 事故形态）→ 必须拒绝，且消息可定位。
out1="$(run_case admin '' 0 2>&1 || true)"
grep -q 'LLM_GATEWAY_ADMIN_PASSWORD is empty' <<<"$out1" \
  || fail "user-set+password-empty did not trip the preflight: $out1"
grep -q 'DL_ALLOW_EMPTY_ADMIN_PASSWORD' <<<"$out1" \
  || fail "preflight message lacks the escape-hatch hint: $out1"

# 2. 用户名 + 密码齐备 → 放行。
out2="$(run_case admin s3cr3t 0)"
grep -q 'PREFLIGHT_PASS' <<<"$out2" || fail "healthy creds wrongly rejected: $out2"

# 3. 用户名已设 + 密码为空 + 逃生口 → 放行（奇局由操作者显式承担）。
out3="$(run_case admin '' 1)"
grep -q 'PREFLIGHT_PASS' <<<"$out3" || fail "escape hatch DL_ALLOW_EMPTY_ADMIN_PASSWORD=true did not bypass: $out3"

# 4. 两值同空 → 放行（维持旧行为，sync 侧 warn+skip 负责提示）。
out4="$(run_case '' '' 0)"
grep -q 'PREFLIGHT_PASS' <<<"$out4" || fail "both-empty wrongly rejected (legacy shape must stay untouched): $out4"

echo 'deploy-local-lib admin-password preflight test: PASS'
