#!/usr/bin/env bash
# scripts/lib/load-env.sh
# 自动加载运维环境变量
# Usage: source "$(dirname "${BASH_SOURCE[0]}")/lib/load-env.sh"

# ------ 密钥互异校验门（N20-6 同款 / R23-A，2026-09-30）------
# 与 scripts/load-env.sh 的 check_secret_key_distinct 同源（09-28 全 503 事故
# 模式防复发）：.env 曾出现 SK 与 CEK 同值 → 容器解密全挂 → dispatch
# ErrNoRoute → 全 503。本变体仅被 source 使用（各分支以 return 出口），
# 校验失败即 return 1 终止调用方。CEK 留空合法（运行时从 SK 派生 keyring）；
# 长度偏离已知形态仅告警不阻塞——base64 两种 padding 形态均合法，
# 未来换钥形态不应被门卡死。
check_secret_key_distinct() {
    local sk="${LLM_GATEWAY_SECRET_KEY:-${SECRET_KEY:-}}"
    local cek="${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-${CREDENTIAL_ENCRYPTION_KEY:-}}"
    if [ -z "$sk" ] || [ -z "$cek" ]; then
        return 0
    fi
    if [ "$sk" = "$cek" ]; then
        echo "[load-env/lib] ❌ 密钥校验失败（N20-6）: SECRET_KEY 与 CREDENTIAL_ENCRYPTION_KEY 同值" >&2
        echo "[load-env/lib]    该组合即 2026-09-28 全 503 事故模式（解密全挂）。请修正环境来源：" >&2
        echo "[load-env/lib]    SK 64 位、CEK 43/44 位且互异；或将 CEK 留空由 SK 派生" >&2
        return 1
    fi
    if [ "${#sk}" -ne 64 ]; then
        echo "[load-env/lib] ⚠️  LLM_GATEWAY_SECRET_KEY 长度 ${#sk}（已知形态 64 位），请人工确认" >&2
    fi
    if [ "${#cek}" -ne 44 ] && [ "${#cek}" -ne 43 ]; then
        echo "[load-env/lib] ⚠️  LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY 长度 ${#cek}（已知形态 43/44 位），请人工确认" >&2
    fi
    return 0
}

if [[ -r /etc/llm-gateway-go/ops-env.sh ]]; then
  source /etc/llm-gateway-go/ops-env.sh
  check_secret_key_distinct || return 1
  return 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [[ -r "$PROJECT_ROOT/.env.local" ]]; then
  set -a
  source "$PROJECT_ROOT/.env.local"
  set +a
  check_secret_key_distinct || return 1
  return 0
fi

if command -v sops >/dev/null 2>&1; then
  if [[ -n "${SOPS_AGE_KEY_FILE:-}" && -r "$PROJECT_ROOT/.env.154.enc" ]]; then
    eval "$(sops -d "$PROJECT_ROOT/.env.154.enc" 2>/dev/null)"
    check_secret_key_distinct || return 1
    return 0
  fi
fi

echo "[WARN] No env loaded" >&2
return 1
