#!/bin/bash
# =====================================================================
# hostedtask-config-gate-check.sh 行为自测（R63）
#
# 用伪造 JWT（openssl base64 现造，无网络、无凭据）驱动 §6.0 自检脚本的
# 各判定分支，断言退出码口径：
#   0 = 特性未启用 / 门禁全在位
#   1 = 存在缺失项（将触发 dispatch_degraded 降级路径）
# =====================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/hostedtask-config-gate-check.sh"
FAILS=0

b64url() { openssl base64 -A 2>/dev/null | tr '+/' '-_' | tr -d '='; }

mk_jwt() { # $1 = payload JSON
  local h p
  h="$(printf '%s' '{"alg":"RS256","typ":"JWT"}' | b64url)"
  p="$(printf '%s' "$1" | b64url)"
  printf '%s.%s.sig' "$h" "$p"
}

now="$(date +%s)"
JWT_GOOD="$(mk_jwt "{\"tenant_id\":\"tenant-A\",\"sub\":\"acc-service\",\"exp\":$((now+3600))}")"
JWT_NO_TENANT="$(mk_jwt "{\"sub\":\"acc-service\",\"exp\":$((now+3600))}")"
JWT_EXPIRED="$(mk_jwt "{\"tenant_id\":\"tenant-A\",\"sub\":\"acc-service\",\"exp\":$((now-3600))}")"

# run_case <name> <want_exit> <env assignments...>
run_case() {
  local name="$1" want="$2"; shift 2
  local rc=0
  env -i PATH="${PATH}" HOME="${HOME}" \
    LLM_GATEWAY_ACC_BASE_URL="http://127.0.0.1:1" \
    "$@" bash "$CHECK" >/dev/null 2>&1 || rc=$?
  if [[ "$rc" -eq "$want" ]]; then
    echo "ok: $name (exit=$rc)"
  else
    echo "FAIL: $name (exit=$rc, want=$want)"
    FAILS=$((FAILS+1))
  fi
}

run_case "特性未启用 → 0（门禁不适用）" 0 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=false
run_case "启用但无任何 ACC 配置 → 1" 1 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true
run_case "门禁3a 全在位（tenant_id+未过期+runtime+workspace）→ 0" 0 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true \
  LLM_GATEWAY_ACC_SERVICE_TOKEN="$JWT_GOOD" \
  LLM_GATEWAY_ACC_RUNTIME_ID="rt-1" \
  LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP="default=/srv/workspace"
run_case "JWT 缺 tenant_id claim → 1（§6.0 门禁3）" 1 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true \
  LLM_GATEWAY_ACC_SERVICE_TOKEN="$JWT_NO_TENANT" \
  LLM_GATEWAY_ACC_RUNTIME_ID="rt-1" \
  LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP="default=/srv/workspace"
run_case "JWT 已过期 → 1" 1 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true \
  LLM_GATEWAY_ACC_SERVICE_TOKEN="$JWT_EXPIRED" \
  LLM_GATEWAY_ACC_RUNTIME_ID="rt-1" \
  LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP="default=/srv/workspace"
run_case "token 非法结构 → 1" 1 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true \
  LLM_GATEWAY_ACC_SERVICE_TOKEN="not-a-jwt" \
  LLM_GATEWAY_ACC_RUNTIME_ID="rt-1" \
  LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP="default=/srv/workspace"
run_case "缺 RUNTIME_ID → 1" 1 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true \
  LLM_GATEWAY_ACC_SERVICE_TOKEN="$JWT_GOOD" \
  LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP="default=/srv/workspace"
run_case "缺 WORKSPACE_MAP → 1" 1 \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true \
  LLM_GATEWAY_ACC_SERVICE_TOKEN="$JWT_GOOD" \
  LLM_GATEWAY_ACC_RUNTIME_ID="rt-1"

# 过期判定必须真的走到 miss 行（而非中途 crash 碰巧 exit 1 的假阳性）：
# 断言输出含"已过期"字样。
EXP_OUT="$(env -i PATH="${PATH}" HOME="${HOME}" \
  LLM_GATEWAY_HOSTED_TASKS_ENABLED=true \
  LLM_GATEWAY_ACC_BASE_URL="http://127.0.0.1:1" \
  LLM_GATEWAY_ACC_SERVICE_TOKEN="$JWT_EXPIRED" \
  LLM_GATEWAY_ACC_RUNTIME_ID="rt-1" \
  LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP="default=/srv/workspace" \
  bash "$CHECK" 2>&1 || true)"
if printf '%s' "$EXP_OUT" | grep -q "已过期"; then
  echo "ok: 过期用例输出含『已过期』判定（非 crash 假阳性）"
else
  echo "FAIL: 过期用例未输出『已过期』判定（疑似中途 crash）"
  FAILS=$((FAILS+1))
fi

# --env-file 装载路径：临时 env 文件含全套合法配置 → 0。
ENVFILE="$(mktemp)"
trap 'rm -f "$ENVFILE"' EXIT
{
  echo "LLM_GATEWAY_HOSTED_TASKS_ENABLED=true"
  echo "LLM_GATEWAY_ACC_BASE_URL=http://127.0.0.1:1"
  echo "LLM_GATEWAY_ACC_SERVICE_TOKEN=$JWT_GOOD"
  echo "LLM_GATEWAY_ACC_RUNTIME_ID=rt-1"
  echo "LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP=default=/srv/workspace"
} > "$ENVFILE"
rc=0
bash "$CHECK" --env-file "$ENVFILE" >/dev/null 2>&1 || rc=$?
if [[ "$rc" -eq 0 ]]; then
  echo "ok: --env-file 装载合法配置 → 0"
else
  echo "FAIL: --env-file 装载合法配置 (exit=$rc, want=0)"
  FAILS=$((FAILS+1))
fi

# token 不回显：输出中不得出现 JWT 任何片段。
OUT="$(bash "$CHECK" --env-file "$ENVFILE" 2>&1 || true)"
if printf '%s' "$OUT" | grep -qF "$JWT_GOOD"; then
  echo "FAIL: 自检输出泄漏了 token 本体"
  FAILS=$((FAILS+1))
else
  echo "ok: 输出不含 token 本体（只出指纹/claim）"
fi

if [[ "$FAILS" -eq 0 ]]; then
  echo "ALL PASS"
  exit 0
fi
echo "$FAILS case(s) FAILED"
exit 1
