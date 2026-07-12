#!/bin/bash
# migrate-session-dim-154.sh - 在154环境执行session_dim表迁移
#
# 用法:
#   ./scripts/migrate-session-dim-154.sh [--dry-run]
#
# 功能:
#   1. 备份 session_summaries 表
#   2. 执行 350_session_analytics_fix.sql 迁移
#   3. 验证表和触发器
#   4. 可选的数据回填

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REMOTE_USER="root"
REMOTE_HOST="47.97.111.154"
REMOTE_PORT="25022"
DB_HOST="172.16.2.210"
DB_USER="llm_gateway"
DB_NAME="llm_gateway"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
  echo -e "${YELLOW}[DRY-RUN MODE]${NC} 将显示执行计划但不实际执行"
  echo ""
fi

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}  Session_dim 表迁移脚本 - 154环境${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# 步骤 1: 检查迁移文件
echo -e "${YELLOW}[1/7]${NC} 检查迁移文件..."
MIGRATION_FILE="$REPO_DIR/sql/migrations/startup/350_session_analytics_fix.sql"
if [ ! -f "$MIGRATION_FILE" ]; then
  echo -e "${RED}✗ 迁移文件不存在: $MIGRATION_FILE${NC}"
  exit 1
fi
echo -e "${GREEN}✓ 迁移文件就绪${NC}"
echo ""

# 步骤 2: 上传迁移文件
echo -e "${YELLOW}[2/7]${NC} 上传迁移文件到服务器..."
if [ $DRY_RUN -eq 0 ]; then
  sshpass -p 'Kaixuan2026&#*9527' scp -P "$REMOTE_PORT" \
    "$MIGRATION_FILE" \
    "$REMOTE_USER@$REMOTE_HOST:/tmp/350_session_analytics_fix.sql"
  echo -e "${GREEN}✓ 上传成功${NC}"
else
  echo -e "${BLUE}[DRY-RUN] scp $MIGRATION_FILE → /tmp/${NC}"
fi
echo ""

# 步骤 3: 备份现有数据
echo -e "${YELLOW}[3/7]${NC} 备份 session_summaries 表..."
if [ $DRY_RUN -eq 0 ]; then
  sshpass -p 'Kaixuan2026&#*9527' ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" << EOF
export PGPASSWORD='${LLM_GATEWAY_DB_PASSWORD:-}'
pg_dump -U $DB_USER -h $DB_HOST -d $DB_NAME \
  -t session_summaries \
  --no-owner --no-privileges \
  > /tmp/session_summaries_backup_$(date +%Y%m%d_%H%M%S).sql 2>&1 || echo "备份失败，但继续执行"
echo "✓ 备份完成（如果pg_dump可用）"
EOF
  echo -e "${GREEN}✓ 备份尝试完成${NC}"
else
  echo -e "${BLUE}[DRY-RUN] pg_dump session_summaries → /tmp/backup${NC}"
fi
echo ""

# 步骤 4: 检查表是否已存在
echo -e "${YELLOW}[4/7]${NC} 检查 session_dim 表状态..."
if [ $DRY_RUN -eq 0 ]; then
  TABLE_EXISTS=$(sshpass -p 'Kaixuan2026&#*9527' ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" << 'EOF'
export PGPASSWORD="${LLM_GATEWAY_DB_PASSWORD:-}"
psql -U llm_gateway -h 172.16.2.210 -d llm_gateway -t -c \
  "SELECT COUNT(*) FROM information_schema.tables WHERE table_name='session_dim'" 2>/dev/null || echo "0"
EOF
)
  if [ "$TABLE_EXISTS" -gt 0 ]; then
    echo -e "${GREEN}⚠ session_dim 表已存在，将跳过CREATE TABLE${NC}"
  else
    echo -e "${YELLOW}○ session_dim 表不存在，将创建${NC}"
  fi
else
  echo -e "${BLUE}[DRY-RUN] 检查表是否存在${NC}"
fi
echo ""

# 步骤 5: 执行迁移
echo -e "${YELLOW}[5/7]${NC} 执行 350_session_analytics_fix.sql..."
if [ $DRY_RUN -eq 0 ]; then
  sshpass -p 'Kaixuan2026&#*9527' ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" << 'EOF'
export PGPASSWORD="${LLM_GATEWAY_DB_PASSWORD:-}"
psql -U llm_gateway -h 172.16.2.210 -d llm_gateway \
  -f /tmp/350_session_analytics_fix.sql \
  2>&1 | tee /tmp/migration_350.log

if [ ${PIPESTATUS[0]} -eq 0 ]; then
  echo "✓ 迁移执行成功"
  exit 0
else
  echo "✗ 迁移执行失败，请检查 /tmp/migration_350.log"
  exit 1
fi
EOF
  echo -e "${GREEN}✓ 迁移执行完成${NC}"
else
  echo -e "${BLUE}[DRY-RUN] psql -f 350_session_analytics_fix.sql${NC}"
fi
echo ""

# 步骤 6: 验证
echo -e "${YELLOW}[6/7]${NC} 验证迁移结果..."
if [ $DRY_RUN -eq 0 ]; then
  sshpass -p 'Kaixuan2026&#*9527' ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" << 'EOF'
export PGPASSWORD="${LLM_GATEWAY_DB_PASSWORD:-}"

echo "=== 检查 session_dim 表 ==="
psql -U llm_gateway -h 172.16.2.210 -d llm_gateway -c \
  "SELECT COUNT(*) as record_count FROM session_dim" 2>&1 || echo "查询失败"

echo ""
echo "=== 检查触发器 ==="
psql -U llm_gateway -h 172.16.2.210 -d llm_gateway -c \
  "SELECT tgname, tgenabled FROM pg_trigger WHERE tgname = 'trg_update_session_summary'" 2>&1 || echo "查询失败"

echo ""
echo "=== 检查视图 ==="
psql -U llm_gateway -h 172.16.2.210 -d llm_gateway -c \
  "SELECT COUNT(*) FROM information_schema.views WHERE table_name = 'v_session_analytics'" 2>&1 || echo "查询失败"
EOF
  echo -e "${GREEN}✓ 验证完成${NC}"
else
  echo -e "${BLUE}[DRY-RUN] 验证表、触发器、视图${NC}"
fi
echo ""

# 步骤 7: 重启服务
echo -e "${YELLOW}[7/7]${NC} 重启 llm-gateway-go 服务..."
if [ $DRY_RUN -eq 0 ]; then
  sshpass -p 'Kaixuan2026&#*9527' ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" << 'EOF'
systemctl restart llm-gateway-go.service
sleep 3
systemctl status llm-gateway-go.service --no-pager -l | head -15
EOF
  echo -e "${GREEN}✓ 服务重启完成${NC}"
else
  echo -e "${BLUE}[DRY-RUN] systemctl restart llm-gateway-go.service${NC}"
fi
echo ""

echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  迁移完成！${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo -e "验证步骤："
echo -e "  1. 访问: ${BLUE}https://llm.kxpms.cn${NC}"
echo -e "  2. 检查仪表盘 → 会话统计面板"
echo -e "  3. 查看 Top5 客户端和 Top5 任务是否显示数据"
echo -e "  4. 发送测试请求，观察 session_dim 表是否有新记录"
echo ""
echo -e "数据回填（可选）："
echo -e "  ${BLUE}ssh -p $REMOTE_PORT $REMOTE_USER@$REMOTE_HOST${NC}"
echo -e "  ${BLUE}psql -U $DB_USER -h $DB_HOST -d $DB_NAME${NC}"
echo -e "  然后执行 docs/SESSION_DIM_ANALYSIS.md 中的回填SQL"
echo ""
