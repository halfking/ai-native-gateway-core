#!/usr/bin/env bash
# =====================================================================
# scripts/upgrade-instance.sh — 实例侧升级统一入口（委托 llm-gw-installer）
#
# 用法:
#   ./scripts/upgrade-instance.sh check
#   ./scripts/upgrade-instance.sh apply [--channel stable|beta|canary]
#   ./scripts/upgrade-instance.sh rollback
#   ./scripts/upgrade-instance.sh apply --offline --offline-package /path/to/pkg.tar.gz
#
# 环境变量:
#   MASTER_URL   主控端地址（默认 https://llm.kxpms.cn）
#   DATA_DIR     数据目录（默认 /opt/llm-gateway-go）
#   INSTANCE_ID  实例 ID（可选，写入升级上报）
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALLER_DIR="$REPO_ROOT/installer"

MASTER_URL="${MASTER_URL:-https://llm.kxpms.cn}"
DATA_DIR="${DATA_DIR:-/opt/llm-gateway-go}"
CHANNEL="${CHANNEL:-stable}"

usage() {
  cat <<EOF
用法: $0 <check|apply|rollback> [options]

  check                     检查可用更新
  apply [--channel CH]      在线升级
  apply --offline --offline-package PATH
  rollback                  回滚到上一版本

环境变量: MASTER_URL DATA_DIR INSTANCE_ID CHANNEL
EOF
}

if [[ $# -eq 0 || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

ACTION="$1"
shift

OFFLINE=false
OFFLINE_PACKAGE=""
EXTRA=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --channel)
      CHANNEL="$2"
      shift 2
      ;;
    --offline)
      OFFLINE=true
      shift
      ;;
    --offline-package)
      OFFLINE_PACKAGE="$2"
      shift 2
      ;;
    *)
      EXTRA+=("$1")
      shift
      ;;
  esac
done

if [[ ! -d "$INSTALLER_DIR" ]]; then
  echo "installer 目录不存在: $INSTALLER_DIR" >&2
  exit 1
fi

cd "$INSTALLER_DIR"

ARGS=(upgrade --action "$ACTION" --master-url "$MASTER_URL" --data-dir "$DATA_DIR" --channel "$CHANNEL")
if [[ "$OFFLINE" == true ]]; then
  ARGS+=(--offline --offline-package "$OFFLINE_PACKAGE")
fi

if [[ ${#EXTRA[@]} -gt 0 ]]; then
  ARGS+=("${EXTRA[@]}")
fi

exec go run ./cmd/llm-gw-installer "${ARGS[@]}"
