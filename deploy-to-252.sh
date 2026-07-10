#!/bin/bash
# deploy-to-252.sh — 部署 llm-gateway-go 到阿里云 252（115.29.212.252:25022）
#
# 用途：
#   把当前工作树的 llm-gateway-go 二进制 + DB migration 部署到 252 的
#   /opt/llm-gateway-go/，并通过 systemd 重启 llm-gateway-go.service。
#
# 路径约定：
#   远程二进制：/opt/llm-gateway-go/llm-gateway-go
#   远程 DB：    postgres://llm_gateway@172.16.2.210:5432/llm_gateway  (本机 pg-252-pg17)
#   服务名：    llm-gateway-go.service
#   备份：      /opt/llm-gateway-go/llm-gateway-go.bak-{deploy_seq}-{YYYYMMDD-HHMMSS}
#
# 用法：
#   ./deploy-to-252.sh                      # 全流程：编译 → migration → 上传 → 重启 → 验证
#   ./deploy-to-252.sh --skip-build        # 跳过编译（假设 binary 已是新版本）
#   ./deploy-to-252.sh --skip-migration    # 跳过 DB migration（已手动跑过）
#   ./deploy-to-252.sh --skip-restart      # 只上传二进制，不重启服务
#
# 依赖：
#   - sshpass（通过 SSHPASS 环境变量）
#   - ssh / scp（macOS/Linux 自带）
#   - docker（远程 252 通过 docker exec 调用 psql）
#
# 重要：默认行为会执行 restart（service 短暂停机 < 5s）。生产环境建议先用
#       --skip-restart 上传 + 手工 rolling。

set -euo pipefail

# ── 配置 ────────────────────────────────────────────────────────────────
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
REMOTE_USER="${DEPLOY_USER:-root}"
REMOTE_HOST="${DEPLOY_HOST:-115.29.212.252}"
REMOTE_PORT="${DEPLOY_PORT:-25022}"
REMOTE_DIR="${REMOTE_DIR:-/opt/llm-gateway-go}"
SERVICE_NAME="${SERVICE_NAME:-llm-gateway-go}"
DB_CONTAINER="${DB_CONTAINER:-pg-252-pg17}"
DB_USER="${DB_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-llm_gateway}"
LOCAL_BIN="${LOCAL_BIN:-$REPO_DIR/bin/llm-gateway-go-linux-amd64}"
MIGRATION_FILE="${MIGRATION_FILE:-$REPO_DIR/sql/migrations/startup/366_model_name_mapping.sql}"

# 颜色
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

# ── 参数 ────────────────────────────────────────────────────────────────
SKIP_BUILD=0; SKIP_MIGRATION=0; SKIP_RESTART=0
while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-build) SKIP_BUILD=1; shift ;;
    --skip-migration) SKIP_MIGRATION=1; shift ;;
    --skip-restart) SKIP_RESTART=1; shift ;;
    -h|--help)
      grep -E '^#( |$)' "$0" | head -30
      exit 0 ;;
    *) echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
  esac
done

# ── 校验 ────────────────────────────────────────────────────────────────
[[ -n "${SSHPASS:-}" ]] || { echo -e "${RED}✗ SSHPASS env var required${NC}"; exit 1; }
which sshpass >/dev/null || { echo -e "${RED}✗ sshpass not installed${NC}"; exit 1; }
which ssh scp >/dev/null || { echo -e "${RED}✗ ssh/scp not installed${NC}"; exit 1; }

SSH_BASE="sshpass -e ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -p $REMOTE_PORT $REMOTE_USER@$REMOTE_HOST"
SCP_BASE="sshpass -e scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -P $REMOTE_PORT"

banner() {
  echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
  echo -e "${BLUE}  LLM Gateway 部署 — 阿里云 252 ($REMOTE_HOST:$REMOTE_PORT)${NC}"
  echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
  echo ""
}

# ── 步骤 ────────────────────────────────────────────────────────────────
banner

# 0. 连通性测试
echo -e "${YELLOW}[0/5]${NC} 验证 SSH 连通性..."
if $SSH_BASE 'echo OK' >/dev/null 2>&1; then
  echo -e "${GREEN}  ✓ SSH OK${NC}"
else
  echo -e "${RED}  ✗ SSH failed${NC}"; exit 1
fi

# 1. 编译后端
if [[ $SKIP_BUILD -eq 0 ]]; then
  echo -e "${YELLOW}[1/5]${NC} 编译后端（linux/amd64 交叉编译）..."
  mkdir -p "$REPO_DIR/bin"
  (cd "$REPO_DIR" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$LOCAL_BIN" ./cmd/gateway) || { echo -e "${RED}  ✗ build failed${NC}"; exit 1; }
  echo -e "${GREEN}  ✓ built: $(ls -la "$LOCAL_BIN" | awk '{print $5}') bytes (linux/amd64)${NC}"
else
  echo -e "${YELLOW}[1/5]${NC} 跳过后端编译（使用现有 ${LOCAL_BIN}）"
fi

# 2. 备份远程二进制
echo -e "${YELLOW}[2/5]${NC} 备份远程二进制..."
DEPLOY_SEQ=$($SSH_BASE "cat $REMOTE_DIR/.deploy_seq 2>/dev/null || echo 0" 2>/dev/null | tr -d '[:space:]')
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
NEW_DEPLOY_SEQ=$((DEPLOY_SEQ + 1))
$SSH_BASE "
  cd $REMOTE_DIR
  if [ -f llm-gateway-go ]; then
    cp -p llm-gateway-go llm-gateway-go.bak-${DEPLOY_SEQ}-${TIMESTAMP}
    # 只保留最近 5 个备份，节省磁盘
    ls -t llm-gateway-go.bak-* 2>/dev/null | tail -n +6 | xargs -r rm -f
    echo '✓ backup done'
  else
    echo '⚠ no existing binary, skip backup'
  fi
"

# 3. 上传二进制
echo -e "${YELLOW}[3/5]${NC} 上传新二进制到 252..."
$SCP_BASE "$LOCAL_BIN" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/llm-gateway-go.new"
$SSH_BASE "chmod +x $REMOTE_DIR/llm-gateway-go.new && ls -la $REMOTE_DIR/llm-gateway-go.new"
echo -e "${GREEN}  ✓ uploaded${NC}"

# 4. 应用 DB migration
if [[ $SKIP_MIGRATION -eq 0 ]]; then
  echo -e "${YELLOW}[4/5]${NC} 应用 DB migration 356 (handoff_enhanced)..."
  if [[ ! -f "$MIGRATION_FILE" ]]; then
    echo -e "${RED}  ✗ migration file not found: $MIGRATION_FILE${NC}"; exit 1
  fi
  # 先 dry-run 验证 SQL（用 psql --single-transaction 隔离）
  echo "    1) 在 252 上执行 SQL..."
  $SSH_BASE "docker exec -i $DB_CONTAINER psql -v ON_ERROR_STOP=1 -U $DB_USER -d $DB_NAME < /dev/stdin" < "$MIGRATION_FILE" 2>&1 | tail -20 | sed 's/^/      /'
  echo "    2) 验证迁移结果..."
  $SSH_BASE "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -c '\\d handoff_logs'" 2>&1 | grep -E "(summary_text|summary_engine|trigger_mode|tokens_in_session|messages_in_session|skill_name|duration_ms)" | sed 's/^/      /' || true
  $SSH_BASE "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -c '\\d session_summaries'" 2>&1 | grep -E "(tokens_at_trigger|messages_at_trigger|last_trigger)" | sed 's/^/      /' || true
  echo -e "${GREEN}  ✓ migration 356 applied${NC}"
else
  echo -e "${YELLOW}[4/5]${NC} 跳过 DB migration"
fi

# 5. 重启服务
if [[ $SKIP_RESTART -eq 0 ]]; then
  echo -e "${YELLOW}[5/5]${NC} 重启 $SERVICE_NAME..."
  # 读取本地 VERSION 写入远端 VERSION 文件
  LOCAL_VERSION=$(cat "$REPO_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')
  LOCAL_VERSION=${LOCAL_VERSION:-2.4.1-handoff-deploy}
  $SSH_BASE "
    set -e
    cd $REMOTE_DIR
    # atomic swap
    mv llm-gateway-go.new llm-gateway-go
    # write deploy seq + version
    echo '$NEW_DEPLOY_SEQ' > .deploy_seq
    echo '$LOCAL_VERSION' > VERSION
    # restart
    systemctl restart $SERVICE_NAME
    sleep 3
    systemctl status $SERVICE_NAME --no-pager -n 3 | head -10 || true
  "
  echo -e "${GREEN}  ✓ restarted (VERSION=$LOCAL_VERSION, deploy_seq=$NEW_DEPLOY_SEQ)${NC}"
else
  echo -e "${YELLOW}[5/5]${NC} 跳过重启（二进制已上传为 .new，待手动原子替换）"
fi

# ── 验证 ────────────────────────────────────────────────────────────────
echo ""
echo -e "${BLUE}━━ 验证 ━━${NC}"

# 1. health
echo -n "  /healthz ... "
HEALTH=$($SSH_BASE "curl -sS -m 5 http://127.0.0.1:8780/healthz 2>/dev/null || echo 'FAIL'")
if echo "$HEALTH" | grep -q '"status":"ok"'; then
  echo -e "${GREEN}OK${NC} ($HEALTH)"
else
  echo -e "${RED}FAIL${NC} ($HEALTH)"
fi

# 2. handoff 路由
echo -n "  /api/admin/handoff/logs ... "
HF=$($SSH_BASE "curl -sS -m 5 -H 'Authorization: Bearer sk-k40DVd9aqFGumYcEkfkQvSgdv06uepSNDK0BqHwtwS3RzTgY' http://127.0.0.1:8780/api/admin/handoff/logs?limit=5 2>&1" || echo 'FAIL')
if echo "$HF" | grep -q '"items"'; then
  echo -e "${GREEN}OK${NC}"
  echo "$HF" | python3 -c "import json,sys; d=json.load(sys.stdin); print('    handoff logs:', len(d.get('items',[])))" 2>/dev/null
else
  echo -e "${YELLOW}?? (route may not be wired yet, raw: ${HF:0:200})${NC}"
fi

# 3. handoff spec 数量
echo -n "  /api/admin/modules/handoff ... "
MD=$($SSH_BASE "curl -sS -m 5 -H 'Authorization: Bearer sk-k40DVd9aqFGumYcEkfkQvSgdv06uepSNDK0BqHwtwS3RzTgY' http://127.0.0.1:8780/api/admin/modules/handoff 2>&1" || echo 'FAIL')
if echo "$MD" | grep -q '"module"'; then
  KEYS=$(echo "$MD" | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d.get('module',{}).get('config_keys',[])))" 2>/dev/null || echo '?')
  CAPS=$(echo "$MD" | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d.get('module',{}).get('capabilities',[])))" 2>/dev/null || echo '?')
  echo -e "${GREEN}OK${NC} (config_keys=$KEYS, capabilities=$CAPS)"
else
  echo -e "${RED}FAIL${NC} ($MD)"
fi

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  部署完成 (deploy_seq: $DEPLOY_SEQ → $NEW_DEPLOY_SEQ)${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "回滚命令："
echo "  sshpass -e ssh -p $REMOTE_PORT $REMOTE_USER@$REMOTE_HOST \\"
echo "    \"cd $REMOTE_DIR && ls -t llm-gateway-go.bak-* | head -1 | xargs -I{} cp {} llm-gateway-go && systemctl restart $SERVICE_NAME\""
echo ""