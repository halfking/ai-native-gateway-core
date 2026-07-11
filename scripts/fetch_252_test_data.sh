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

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -p "$REMOTE_PORT")
REMOTE_SQL="/tmp/extract_session_test_data.$$.$RANDOM.sql"
OUTPUT_DIR="$REPO_DIR/test-data"
OUTPUT_FILE="$OUTPUT_DIR/session_test_data.jsonl"
TEMP_OUTPUT="$OUTPUT_FILE.tmp.$$"

mkdir -p "$OUTPUT_DIR"
cleanup() {
  rm -f "$TEMP_OUTPUT"
  ssh "${SSH_OPTS[@]}" "$REMOTE_USER@$REMOTE_HOST" "rm -f '$REMOTE_SQL'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# 1. 上传SQL脚本
echo -e "${YELLOW}[1/3] 上传SQL脚本到252...${NC}"
scp -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -P "$REMOTE_PORT" \
  "$REPO_DIR/scripts/extract_session_test_data.sql" \
  "$REMOTE_USER@$REMOTE_HOST:$REMOTE_SQL"

# 2. 在252上执行SQL并获取结果
echo -e "${YELLOW}[2/3] 执行SQL查询...${NC}"
ssh "${SSH_OPTS[@]}" "$REMOTE_USER@$REMOTE_HOST" \
  "docker exec -i '$DB_CONTAINER' psql -v ON_ERROR_STOP=1 -U '$DB_USER' -d '$DB_NAME' < '$REMOTE_SQL'" \
  > "$TEMP_OUTPUT"

if [[ ! -s "$TEMP_OUTPUT" ]]; then
  echo -e "${RED}✗ 252没有可回放的多轮会话记录；未覆盖现有数据文件${NC}"
  exit 2
fi

jq -e -c . "$TEMP_OUTPUT" >/dev/null
mv "$TEMP_OUTPUT" "$OUTPUT_FILE"

# 3. 统计
LINES=$(wc -l < "$OUTPUT_FILE" | tr -d ' ')
echo -e "${GREEN}[3/3] ✓ 提取完成: $LINES 条记录${NC}"
echo -e "${GREEN}  保存位置: test-data/session_test_data.jsonl${NC}"

# 4. 显示会话统计
echo -e "${YELLOW}会话统计:${NC}"
jq -r '.session_id' "$OUTPUT_FILE" | sort | uniq -c | sort -rn | head -10

echo -e "${GREEN}✓ 数据提取完成${NC}"
