#!/usr/bin/env bash
# migrate-session-dim-154.sh - 在 154 环境执行 session_dim 表迁移
#
# 用法:
#   ./scripts/migrate-session-dim-154.sh --dry-run
#   LLM_GATEWAY_154_DB_PASSWORD=... \
#   LLM_GATEWAY_MIGRATION_CONFIRM_154=apply \
#   ./scripts/migrate-session-dim-154.sh
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REMOTE_USER="${LLM_GATEWAY_154_SSH_USER:-root}"
REMOTE_HOST="${LLM_GATEWAY_154_SSH_HOST:-47.97.111.154}"
REMOTE_PORT="${LLM_GATEWAY_154_SSH_PORT:-25022}"
DB_HOST="${LLM_GATEWAY_154_DB_HOST:-172.16.2.210}"
DB_PORT="${LLM_GATEWAY_154_DB_PORT:-5432}"
DB_USER="${LLM_GATEWAY_154_DB_USER:-llm_gateway}"
DB_NAME="${LLM_GATEWAY_154_DB_NAME:-llm_gateway}"
MIGRATION_FILE="$REPO_DIR/sql/migrations/350_session_analytics_fix.sql"
REMOTE_MIGRATION_FILE="/tmp/350_session_analytics_fix.sql"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

DRY_RUN=0
case "${1:-}" in
  --dry-run) DRY_RUN=1 ;;
  "") ;;
  *) echo "usage: $0 [--dry-run]" >&2; exit 2 ;;
esac

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$REMOTE_PORT")
if [[ -n "${LLM_GATEWAY_154_SSH_IDENTITY_FILE:-}" ]]; then
  SSH_OPTS+=(-i "$LLM_GATEWAY_154_SSH_IDENTITY_FILE")
fi

remote_target="$REMOTE_USER@$REMOTE_HOST"
remote_ssh() {
  ssh "${SSH_OPTS[@]}" "$remote_target" "$@"
}

remote_db() {
  local command=$1
  printf '%s\n' "$LLM_GATEWAY_154_DB_PASSWORD" |
    ssh "${SSH_OPTS[@]}" "$remote_target" \
      "IFS= read -r PGPASSWORD; export PGPASSWORD; set -euo pipefail; $command"
}

if [[ "$DRY_RUN" -eq 0 ]]; then
  : "${LLM_GATEWAY_154_DB_PASSWORD:?LLM_GATEWAY_154_DB_PASSWORD must be set}"
  : "${LLM_GATEWAY_MIGRATION_CONFIRM_154:?set LLM_GATEWAY_MIGRATION_CONFIRM_154=apply to execute}"
  [[ "$LLM_GATEWAY_MIGRATION_CONFIRM_154" == "apply" ]] || {
    echo "LLM_GATEWAY_MIGRATION_CONFIRM_154 must equal apply" >&2
    exit 2
  }
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  cat <<PLAN
[DRY-RUN] 将执行以下步骤（不连接远端）：
  1. 上传 $MIGRATION_FILE 至 $REMOTE_MIGRATION_FILE
  2. 备份 session_summaries
  3. 检查目标表是否已存在
  4. 执行迁移、验证表/触发器/视图
  5. 重启 llm-gateway-go.service
PLAN
  exit 0
fi

[[ -f "$MIGRATION_FILE" ]] || { echo "迁移文件不存在: $MIGRATION_FILE" >&2; exit 1; }

echo -e "${YELLOW}[1/6]${NC} 上传迁移文件..."
scp "${SSH_OPTS[@]}" "$MIGRATION_FILE" "$remote_target:$REMOTE_MIGRATION_FILE"
echo -e "${GREEN}✓ 上传成功${NC}"

echo -e "${YELLOW}[2/6]${NC} 备份 session_summaries..."
remote_db "pg_dump -h '$DB_HOST' -p '$DB_PORT' -U '$DB_USER' -d '$DB_NAME' -t session_summaries --no-owner --no-privileges > /tmp/session_summaries_backup_\$(date +%Y%m%d_%H%M%S).sql"
echo -e "${GREEN}✓ 备份完成${NC}"

echo -e "${YELLOW}[3/6]${NC} 检查 session_dim 是否已存在..."
table_exists=$(remote_db "psql -X -v ON_ERROR_STOP=1 -h '$DB_HOST' -p '$DB_PORT' -U '$DB_USER' -d '$DB_NAME' -tAc \"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'session_dim');\"" | tr -d '[:space:]')
if [[ "$table_exists" == "t" ]]; then
  echo -e "${YELLOW}session_dim 已存在；停止以避免重复应用迁移${NC}"
  exit 1
fi

echo -e "${YELLOW}[4/6]${NC} 执行迁移..."
remote_db "psql -X -v ON_ERROR_STOP=1 -h '$DB_HOST' -p '$DB_PORT' -U '$DB_USER' -d '$DB_NAME' -f '$REMOTE_MIGRATION_FILE' | tee /tmp/migration_350.log"
echo -e "${GREEN}✓ 迁移执行完成${NC}"

echo -e "${YELLOW}[5/6]${NC} 验证迁移结果..."
remote_db "psql -X -v ON_ERROR_STOP=1 -h '$DB_HOST' -p '$DB_PORT' -U '$DB_USER' -d '$DB_NAME' -c \"SELECT COUNT(*) AS record_count FROM session_dim; SELECT tgname, tgenabled FROM pg_trigger WHERE tgname = 'trg_update_session_summary'; SELECT COUNT(*) AS view_count FROM information_schema.views WHERE table_name = 'v_session_analytics';\""
echo -e "${GREEN}✓ 验证完成${NC}"

echo -e "${YELLOW}[6/6]${NC} 重启 llm-gateway-go 服务..."
remote_ssh "systemctl restart llm-gateway-go.service && sleep 3 && systemctl is-active --quiet llm-gateway-go.service"
echo -e "${GREEN}✓ 服务重启完成${NC}"

echo -e "${GREEN}迁移完成。${NC}"
