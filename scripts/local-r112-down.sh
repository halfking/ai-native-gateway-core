#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# R1.12 本地测试环境停止脚本
#
# 默认: 停止容器, 保留 volumes (重启后数据还在)
# 加 --volumes / -v: 同时清理 volumes (清空 PG / Redis 数据)
#
# 用法:
#   ./scripts/local-r112-down.sh           # 停止 + 保留数据
#   ./scripts/local-r112-down.sh --volumes # 停止 + 清数据
#   ./scripts/local-r112-down.sh -v        # 同上 (短选项)
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.local-r112.yml"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

# 选择 compose 命令
if docker compose version >/dev/null 2>&1; then
  COMPOSE_CMD="docker compose"
else
  COMPOSE_CMD="docker-compose"
fi

cd "$ROOT_DIR"

CLEAN_VOLUMES=0
case "${1:-}" in
  --volumes|-v) CLEAN_VOLUMES=1 ;;
  "")           CLEAN_VOLUMES=0 ;;
  *)
    err "未知参数: $1"
    echo "用法: $0 [--volumes|-v]"
    exit 1
    ;;
esac

info "停止 R1.12 环境..."
$COMPOSE_CMD -f "$COMPOSE_FILE" down

if [ "$CLEAN_VOLUMES" -eq 1 ]; then
  info "清理 volumes (r112_pg_data, r112_redis_data)..."
  $COMPOSE_CMD -f "$COMPOSE_FILE" down -v
  docker volume rm r112_pg_data r112_redis_data 2>/dev/null || true
  ok "volumes 已清理"
else
  ok "容器已停止 (volumes 保留: docker volume ls | grep r112_)"
fi

# 清理 docker compose 创建的网络 (idempotent)
docker network rm r112_net 2>/dev/null || true

ok "Local R1.12 environment stopped"
echo
if [ "$CLEAN_VOLUMES" -eq 1 ]; then
  echo "  下次启动: ./scripts/local-r112-up.sh   (PG/Redis 数据已清空)"
else
  echo "  下次启动: ./scripts/local-r112-up.sh   (PG/Redis 数据保留)"
  echo "  强制清空: ./scripts/local-r112-down.sh --volumes"
fi
