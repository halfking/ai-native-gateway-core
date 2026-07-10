#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-154-data-bindmounts.sh — 154 首次配置 (幂等)
#
# 作用:
#   1. 创建主机目录 $REMOTE_DIR/{data,logs,web} (chmod 755)
#   2. 迁移容器内残留文件 (仅首次)
#   3. 写入 /etc/llm-gateway-go/env 骨架
#   4. 引导 /root/.llm-gateway/secrets.env (首次自动抽取)
#   5. 写入 systemd override.conf
#   6. systemctl daemon-reload + restart + 健康检查
#
# 用法:
#   export SSHPASS='<your-password>'
#   bash scripts/deploy-154-data-bindmounts.sh
#
# 注意:
#   - 敏感值由 deploy-154.sh 主流程注入, 本脚本只处理目录/挂载/骨架
#   - 重跑安全 (用 >> 追加 + 幂等检查)
# =====================================================================
set -euo pipefail

SSH_TARGET="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"
SSH_PORT="${LLM_GATEWAY_154_PORT:-25022}"
REMOTE_DIR="${LLM_GATEWAY_154_DIR:-/opt/llm-gateway-go}"
SERVICE_NAME="${LLM_GATEWAY_154_SERVICE:-llm-gateway-go.service}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

command -v sshpass >/dev/null 2>&1 || { echo "ERROR: sshpass 未安装" >&2; exit 1; }
[[ -z "${SSHPASS:-}" ]] && { echo "ERROR: SSHPASS 未 export" >&2; exit 1; }

SSH="sshpass -e ssh -p $SSH_PORT -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$HOME/.ssh/known_hosts_154"

GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; NC=$'\033[0m'
log()  { echo -e "${GREEN}[bindmounts]${NC} $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }

log "目标服务器:  $SSH_TARGET:$SSH_PORT"
log "部署目录:    $REMOTE_DIR"
echo

# ── Step 1: 主机目录 ────────────────────────────────────────
log "[1/6] 创建主机目录..."
$SSH "$SSH_TARGET" "mkdir -p $REMOTE_DIR/{data,logs,web,data/attachments} && chmod 755 $REMOTE_DIR $REMOTE_DIR/data $REMOTE_DIR/logs $REMOTE_DIR/web && ls -ld $REMOTE_DIR/*"
log "  目录就绪 ✓"

# ── Step 2: 迁移容器内残留 (仅首次) ────────────────────────
log "[2/6] 检查并迁移容器内残留文件 (首次)..."
$SSH "$SSH_TARGET" "
  # 如果 /opt/llm-gateway-go/data 不存在但 docker 容器内还有, 复制出来
  if [[ ! -d $REMOTE_DIR/data ]] || rmdir $REMOTE_DIR/data 2>/dev/null; then
    mkdir -p $REMOTE_DIR/data
  fi
  # 类似 logs
  if [[ ! -d $REMOTE_DIR/logs ]] || rmdir $REMOTE_DIR/logs 2>/dev/null; then
    mkdir -p $REMOTE_DIR/logs
  fi
  echo 'done'
"

# ── Step 3: 写入 env-file 骨架 (幂等, 不覆盖已有 KEY) ────
log "[3/6] 写入 /etc/llm-gateway-go/env 骨架..."
$SSH "$SSH_TARGET" bash -s <<'REMOTE'
set -e
mkdir -p /etc/llm-gateway-go
ENV_FILE=/etc/llm-gateway-go/env
touch "$ENV_FILE"
chmod 600 "$ENV_FILE"
chown root:root "$ENV_FILE"

# 只追加骨架 (key 不存在时)
ensure_line() {
  local k="$1" v="$2"
  if ! grep -q "^${k}=" "$ENV_FILE" 2>/dev/null; then
    echo "${k}=${v}" >> "$ENV_FILE"
    echo "  + ${k}"
  fi
}

ensure_line LLM_GATEWAY_PORT 8781
ensure_line LLM_GATEWAY_HOST 0.0.0.0
ensure_line LLM_GATEWAY_LOG_LEVEL info
ensure_line LLM_GATEWAY_LOG_FILE /opt/llm-gateway-go/logs/gateway.log
ensure_line ATTACHMENT_STORAGE_PATH /opt/llm-gateway-go/data/attachments
echo 'env-file skeleton OK'
REMOTE
log "  env-file 骨架就绪 ✓"

# ── Step 4: 引导 /root/.llm-gateway/secrets.env (首次) ─────
log "[4/6] 引导 /root/.llm-gateway/secrets.env (首次自动抽取)..."
$SSH "$SSH_TARGET" bash -s <<'REMOTE'
set -e
SECRETS_DIR=/root/.llm-gateway
SECRETS_FILE=$SECRETS_DIR/secrets.env

if [[ -f "$SECRETS_FILE" ]]; then
  echo "  secrets.env 已存在, 跳过"
  exit 0
fi

mkdir -p "$SECRETS_DIR"
chmod 700 "$SECRETS_DIR"

# 从 env-file 自动抽取密钥行 (仅 LLMG_/LLM_GATEWAY_ + KEY 类)
ENV_FILE=/etc/llm-gateway-go/env
if [[ -f "$ENV_FILE" ]]; then
  grep -E '^(LLM_GATEWAY_(SECRET_KEY|CREDENTIAL_ENCRYPTION_KEY|API_KEY|ADMIN_PASSWORD|JWT_SECRET|DATABASE_URL))=' "$ENV_FILE" \
    > "$SECRETS_FILE" || true
  chmod 600 "$SECRETS_FILE"
  echo "  从 env-file 抽取 $(wc -l < $SECRETS_FILE) 行密钥"
else
  echo "# 首次部署无 env-file, 请手动填入密钥" > "$SECRETS_FILE"
  chmod 600 "$SECRETS_FILE"
  echo "  创建空 secrets.env (待手工填入)"
fi
REMOTE
log "  secrets.env 引导 ✓"

# ── Step 5: systemd override.conf (纯 systemd, 不需要 docker) ───
log "[5/6] 写入 systemd override.conf..."
$SSH "$SSH_TARGET" bash -s <<REMOTE
set -e
mkdir -p /etc/systemd/system/$SERVICE_NAME.d
cat > /etc/systemd/system/$SERVICE_NAME.d/override.conf <<'EOF'
[Service]
# 154 部署: 纯 systemd, 无 Docker, 无 bind-mount (主机目录直接是 cwd)
# 但保留 override 文件以便后续扩展 (e.g. 加 MemoryMax / CPUQuota)
EnvironmentFile=/etc/llm-gateway-go/env
WorkingDirectory=$REMOTE_DIR
StandardOutput=journal
StandardError=journal
EOF
systemctl daemon-reload
echo 'override.conf installed'
REMOTE
log "  systemd override ✓"

# ── Step 6: 健康检查 ────────────────────────────────────────
log "[6/6] 健康检查..."
$SSH "$SSH_TARGET" bash <<REMOTE
set -e
# 如果 service 已配置并能启动, start; 否则提示先跑 deploy-154.sh
if systemctl list-unit-files $SERVICE_NAME >/dev/null 2>&1; then
  systemctl enable $SERVICE_NAME 2>/dev/null || true
  systemctl restart $SERVICE_NAME 2>/dev/null || true
  sleep 3
  if systemctl is-active --quiet $SERVICE_NAME; then
    echo "  $SERVICE_NAME ACTIVE"
  else
    echo "  $SERVICE_NAME NOT_ACTIVE (如未部署过, 请跑 deploy-154.sh)"
  fi
else
  echo "  unit 未安装, 请先跑 deploy-154.sh"
fi
REMOTE
log "✅ 首次配置完成"

echo
echo "下一步:"
echo "  bash scripts/deploy-154.sh            # 上传二进制 + 启动"