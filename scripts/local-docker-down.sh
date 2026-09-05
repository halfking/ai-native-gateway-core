#!/usr/bin/env bash
# ============================================================================
# scripts/local-docker-down.sh — 本机 Docker 全栈停机
#
# 与 local-docker-up.sh 配套。保留 bind-mount 数据目录（attachments/backups/
# logs/raw-logs/postgres/redis），仅停止并删除容器。可选 --purge 同时删除
# 数据卷。
#
# 用法:
#   bash scripts/local-docker-down.sh             # 仅停容器
#   bash scripts/local-docker-down.sh --purge     # 停容器 + 删数据卷
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=local-host-layout-helper.sh
source "$SCRIPT_DIR/local-host-layout-helper.sh"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; NC=$'\033[0m'
log()  { echo -e "[docker-down] $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }

INSTALL_ROOT="${INSTALL_ROOT:-${LLM_GATEWAY_HOME:-$(lh_root)}}"
COMPOSE_FILE="$INSTALL_ROOT/compose.yml"
COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-llm-gateway-go-local}"
ENV_FILE="$INSTALL_ROOT/.env"

if [[ ! -f "$COMPOSE_FILE" ]]; then
  err "未找到 $COMPOSE_FILE（先跑 scripts/local-docker-up.sh）"
  exit 1
fi

log "停止容器 (保留数据) ..."
docker compose --project-directory "$INSTALL_ROOT" -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" --env-file "$ENV_FILE" down

if [[ "${1:-}" == "--purge" ]]; then
  warn "--purge 模式: 同时删除数据卷 (postgres/data + redis/data)"
  docker compose --project-directory "$INSTALL_ROOT" -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" --env-file "$ENV_FILE" down -v
  rm -rf "$INSTALL_ROOT/postgres" "$INSTALL_ROOT/redis"
  ok "数据卷已清理"
fi

ok "已停机。数据保留在 $INSTALL_ROOT/{attachments,app/logs,raw-logs,backups}"
echo ""
echo "重新启动: bash scripts/local-docker-up.sh"