#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-k8s.sh — K8s / k3s 部署统一入口
#
# 用法:
#   ./scripts/deploy-k8s.sh 252 [--dry-run] [--skip-build] [--with-migration]
#   ./scripts/deploy-k8s.sh kaixuan-1 [--dry-run]
#   ./scripts/deploy-k8s.sh verify 252
#   ./scripts/deploy-k8s.sh rollback 252
#   ./scripts/deploy-k8s.sh migrate 252
#
# 说明:
#   - 252: 公网 k3s 数据面
#   - kaixuan-1/2/3: 内网 k3s 集群
#   - 底层委托 scripts/deploy.sh，保持与主机部署相同的版本 bump / 验证流程
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SH="$SCRIPT_DIR/deploy.sh"

usage() {
  cat <<EOF
用法: $0 <target|action> [options]

target (k3s):
   252 | kaixuan-1 | kaixuan-2 | kaixuan-3

action:
  verify <target>
  rollback <target>
  migrate <target>

options (透传给 deploy.sh):
  --dry-run --skip-build --skip-tests --with-migration --no-rollback --seq <n>

示例:
  $0 252 --dry-run
  $0 verify 252
EOF
}

if [[ $# -eq 0 || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

case "${1:-}" in
  verify|rollback|migrate)
    action="$1"
    target="${2:-}"
    if [[ -z "$target" ]]; then
      echo "缺少 target: $0 $action <252|kaixuan-1>" >&2
      exit 64
    fi
    shift 2
    exec "$DEPLOY_SH" "$action" "$target" "$@"
    ;;
   252|kaixuan-1|kaixuan-2|kaixuan-3)
    target="$1"
    shift
    exec "$DEPLOY_SH" "$target" "$@"
    ;;
  *)
    echo "未知 target/action: $1" >&2
    usage
    exit 64
    ;;
esac
