#!/bin/bash
# fetch_252_test_data.sh — 从252数据库提取会话测试数据
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
REMOTE_HOST="${DEPLOY_HOST:-115.29.212.252}"
REMOTE_PORT="${DEPLOY_PORT:-25022}"
REMOTE_USER="${DEPLOY_USER:-root}"
DB_CONTAINER="pg-252-pg17"
DB_USER="llm_gateway"
DB_NAME="llm_gateway"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${YELLOW}从252数据库提取会话测试数据...${NC}"

# 检查 SSHPASS
[[ -n "${SSHPASS:-}" ]] || { echo -e "${RED}✗ SSHPASS env var required${NC}"; exit 1; }

SSH_BASE="sshpass -e ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p $REMOTE_PORT $REMOTE_USER@$REMOTE_HOST"

# 1. 上传SQL脚本
echo -e "${YELLOW}[1/3] 上传SQL脚本到252...${NC}"
sshpass -e scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -P $REMOTE_PORT \
  "$REPO_DIR/scripts/extract_session_test_data.sql" \
  "$REMOTE_USER@$REMOTE_HOST:/tmp/extract_session_test_data.sql"

# 2. 在252上执行SQL并获取结果
echo -e "${YELLOW}[2/3] 执行SQL查询...${NC}"
$SSH_BASE "docker exec -i $DB_CONTAINER psql -U $DB_USER -d $DB_NAME < /tmp/extract_session_test_data.sql" > "$REPO_DIR/test-data/session_test_data.jsonl"

# 3. 统计
LINES=$(wc -l < "$REPO_DIR/test-data/session_test_data.jsonl" | tr -d ' ')
echo -e "${GREEN}[3/3] ✓ 提取完成: $LINES 条记录${NC}"
echo -e "${GREEN}  保存位置: test-data/session_test_data.jsonl${NC}"

# 4. 显示会话统计
echo -e "${YELLOW}会话统计:${NC}"
jq -r '.session_id' "$REPO_DIR/test-data/session_test_data.jsonl" | sort | uniq -c | sort -rn | head -10

echo -e "${GREEN}✓ 数据提取完成${NC}"
