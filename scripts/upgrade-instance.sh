#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
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
