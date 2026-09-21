#!/usr/bin/env bash
# =====================================================================
# scripts/clear-upgrade-banner.sh — 手动清除升级页面
#
# 用法:
#   bash scripts/clear-upgrade-banner.sh 245
#   bash scripts/clear-upgrade-banner.sh 154
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

TARGET="${1:-}"
[[ -n "$TARGET" ]] || { echo "用法: clear-upgrade-banner.sh <245|154>" >&2; exit 1; }

case "$TARGET" in
  154|245) ;;
  *) echo "不支持的目标: $TARGET (仅 154|245)" >&2; exit 1 ;;
esac

# Load envs
ENVS_ROOT="${ENVS_ROOT:-${HOME}/workspace/ai-native-tools/envs}"
case "$TARGET" in
  245) ENV_SERVER="8.136.114.245" ;;
  154) ENV_SERVER="47.97.111.154" ;;
esac

if [[ ! -f "$ENVS_ROOT/loader.sh" ]]; then
  echo "envs SSOT loader not found: $ENVS_ROOT/loader.sh" >&2
  exit 1
fi
source "$ENVS_ROOT/loader.sh" --all --project llm-gateway-go --server "$ENV_SERVER"

# SSH setup
SSH_PORT="${LLM_GATEWAY_SSH_PORT:-${SSH_PORT:-25022}}"
case "$TARGET" in
  245) SSH_KEY_FILE="${SSH_KEY_245:-${SSH_KEY_FILE:-}}" ;;
  154) SSH_KEY_FILE="${SSH_KEY_154:-${SSH_KEY_FILE:-}}" ;;
esac

if [[ -z "$SSH_KEY_FILE" || ! -f "$SSH_KEY_FILE" ]]; then
  echo "missing SSH key for target $TARGET" >&2
  exit 1
fi

echo "清除 $TARGET 的升级页面..."

# Clear main server
ssh -i "$SSH_KEY_FILE" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
  root@"$ENV_SERVER" \
  "set -e; rm -f /opt/llm-gateway-go/maintenance/index.html; test ! -e /opt/llm-gateway-go/maintenance/index.html; rm -f /opt/llm-gateway-go/maintenance/UPGRADING" \
  && echo "  ✓ $TARGET 升级静态页已清除"

# Clear 252 proxy if target is 154
if [[ "$TARGET" == "154" ]]; then
  echo "清除 252 代理服务器的升级页面..."
  SSH_KEY_252="${SSH_KEY_252:-${HOME}/.ssh/id_ed25519}"
  if [[ -f "$SSH_KEY_252" ]]; then
    ssh -i "$SSH_KEY_252" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
      root@115.29.212.252 \
      "set -e; rm -f /var/www/llm-gateway-maintenance/index.html; test ! -e /var/www/llm-gateway-maintenance/index.html; rm -f /var/www/llm-gateway-maintenance/UPGRADING" \
      && echo "  ✓ 252 代理升级静态页已清除"
  else
    echo "  ⚠ 252 SSH key not found: $SSH_KEY_252" >&2
  fi
fi

echo "完成！请刷新浏览器验证升级页面已消失。"
