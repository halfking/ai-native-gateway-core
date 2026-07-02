#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# 停止本地环境
#
# 用法:
#   ./scripts/local-down.sh           # 停止容器, 保留 volumes
#   ./scripts/local-down.sh --volumes # 停止 + 清空 PG/Redis 数据
#   ./scripts/local-down.sh -v        # 同上
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.local-r112.yml"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

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
  *) err "未知参数: $1"; echo "用法: $0 [--volumes|-v]"; exit 1 ;;
esac

info "停止本地环境..."
$COMPOSE_CMD -f "$COMPOSE_FILE" down

if [ "$CLEAN_VOLUMES" = "1" ]; then
  info "清理 volumes (r112_pg_data, r112_redis_data)..."
  $COMPOSE_CMD -f "$COMPOSE_FILE" down -v
  docker volume rm r112_pg_data r112_redis_data 2>/dev/null || true
  ok "volumes 已清理"
else
  ok "容器已停止 (volumes 保留)"
fi

docker network rm r112_net 2>/dev/null || true

ok "本地环境已停止"
echo
if [ "$CLEAN_VOLUMES" = "1" ]; then
  echo "  重启: ./scripts/local-up.sh   (PG/Redis 数据已清空)"
else
  echo "  重启: ./scripts/local-up.sh   (PG/Redis 数据保留)"
fi
