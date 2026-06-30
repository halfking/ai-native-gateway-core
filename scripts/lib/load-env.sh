#!/usr/bin/env bash
# scripts/lib/load-env.sh
# 自动加载运维环境变量
# Usage: source "$(dirname "${BASH_SOURCE[0]}")/lib/load-env.sh"

if [[ -r /etc/llm-gateway-go/ops-env.sh ]]; then
  source /etc/llm-gateway-go/ops-env.sh
  return 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [[ -r "$PROJECT_ROOT/.env.local" ]]; then
  set -a
  source "$PROJECT_ROOT/.env.local"
  set +a
  return 0
fi

if command -v sops >/dev/null 2>&1; then
  if [[ -n "${SOPS_AGE_KEY_FILE:-}" && -r "$PROJECT_ROOT/.env.71.enc" ]]; then
    eval "$(sops -d "$PROJECT_ROOT/.env.71.enc" 2>/dev/null)"
    return 0
  fi
fi

echo "[WARN] No env loaded" >&2
return 1
