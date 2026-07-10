#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-154.sh — 一键部署到阿里云 154 服务器 (47.97.111.154)
#
# 流程（8 步）:
#   1. 预检 (git clean / go.mod / ssh 连通)
#   2. bump-version (修复漂移 + +1)
#   3. 前端 npm ci + vite build
#   4. Go 交叉编译 linux/amd64 (ldflags 注入版本号)
#   5. sshpass stop 154 上的 llm-gateway-go.service (避免 mmap 锁)
#   6. scp 二进制 + web/dist + VERSION + .deploy_seq 到 154
#   7. sshpass 写入 /etc/llm-gateway-go/env (含 252 PG DSN) + daemon-reload + start
#   8. smoke-verify (curl /healthz + /api/system/version)
#
# 用法:
#   export SSHPASS='<your-password>'
#   bash scripts/deploy-154.sh                    # 自动 +1 build_seq
#   bash scripts/deploy-154.sh --seq 950          # 强制设定
#   bash scripts/deploy-154.sh --no-frontend      # 跳过前端构建
#   bash scripts/deploy-154.sh --ssh root@47.97.111.154 --port 25022
#
# 前置 (硬门禁):
#   env-injector inject aliyun-gateway-154
#   ↑ 必须先注入 SSH_KEY_154 / LLM_GATEWAY_SECRET_KEY 等
# =====================================================================
set -euo pipefail

# ── 默认值 ──────────────────────────────────────────────────────
SSH_TARGET="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"
SSH_PORT="${LLM_GATEWAY_154_PORT:-25022}"
REMOTE_DIR="${LLM_GATEWAY_154_DIR:-/opt/llm-gateway-go}"
SERVICE_NAME="${LLM_GATEWAY_154_SERVICE:-llm-gateway-go.service}"
BIN_NAME="llm-gateway-go.v322.linux.amd64"
SKIP_FRONTEND=false
SKIP_BUMP=false
TARGET_SEQ=""
DRY_RUN=false

# ── 参数解析 ────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --seq)           TARGET_SEQ="$2"; shift 2 ;;
    --no-frontend)   SKIP_FRONTEND=true; shift ;;
    --no-bump)       SKIP_BUMP=true; shift ;;
    --ssh)           SSH_TARGET="$2"; shift 2 ;;
    --port)          SSH_PORT="$2"; shift 2 ;;
    --dry-run)       DRY_RUN=true; shift ;;
    -h|--help)
      sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "deploy-154: 未知参数 $1" >&2; exit 1 ;;
  esac
done

# ── 路径 ────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; RED=$'\033[0;31m'; NC=$'\033[0m'
log()  { echo -e "${GREEN}[deploy-154]${NC} $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }
err()  { echo -e "${RED}[error]${NC} $*" >&2; }

# ── 硬门禁：env-injector 已注入 ────────────────────────────────
for v in LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_DATABASE_URL; do
  if [[ -z "${!v:-}" ]]; then
    err "环境变量 $v 未设置"
    err "请先执行: env-injector inject aliyun-gateway-154"
    exit 2
  fi
done

# sshpass
if ! command -v sshpass >/dev/null 2>&1; then
  err "sshpass 未安装 (brew install sshpass)"
  exit 2
fi
if [[ -z "${SSHPASS:-}" ]]; then
  err "SSHPASS 未 export"
  err "export SSHPASS='<your-password>'"
  exit 2
fi

SSH="sshpass -e ssh -p $SSH_PORT -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$HOME/.ssh/known_hosts_154"
SCP="sshpass -e scp -P $SSH_PORT -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$HOME/.ssh/known_hosts_154"

log "目标服务器:  $SSH_TARGET:$SSH_PORT"
log "部署目录:    $REMOTE_DIR"
log "服务名:      $SERVICE_NAME"
log "二进制:      $BIN_NAME"
log "skip frontend: $SKIP_FRONTEND"
echo

# ── Step 1: 预检 ───────────────────────────────────────────────
log "[1/8] 预检..."
[[ -f go.mod ]] || { err "go.mod 不存在"; exit 1; }
git diff --quiet HEAD 2>/dev/null || { err "工作区有未提交改动"; exit 1; }
git diff --cached --quiet HEAD 2>/dev/null || { err "有 staged 但未提交改动"; exit 1; }
log "  git clean: ✓"

$SSH "$SSH_TARGET" "echo connected && uname -a" >/dev/null || { err "SSH 不可达"; exit 1; }
log "  ssh OK: ✓"

# ── Step 2: bump-version ──────────────────────────────────────
if [[ "$SKIP_BUMP" == "true" ]]; then
  log "[2/8] 跳过 bump-version (--no-bump)"
  # 从 version.json 读当前 seq/version
  NEW_SEQ=$(python3 -c "import json; print(json.load(open('version.json'))['build_seq'])")
  NEW_VERSION=$(python3 -c "import json; print(json.load(open('version.json'))['version'])")
  HEAD_SHA=$(python3 -c "import json; print(json.load(open('version.json'))['git_sha'])")
  HEAD_DATE=$(python3 -c "import json; print(json.load(open('version.json'))['build_date'])")
  log "  current seq=$NEW_SEQ version=$NEW_VERSION"
else
  log "[2/8] bump-version..."
  flags=()
  [[ -n "$TARGET_SEQ" ]] && flags+=(--seq "$TARGET_SEQ")
  bash scripts/bump-version.sh "${flags[@]}"
  NEW_SEQ=$(python3 -c "import json; print(json.load(open('version.json'))['build_seq'])")
  NEW_VERSION=$(python3 -c "import json; print(json.load(open('version.json'))['version'])")
  HEAD_SHA=$(python3 -c "import json; print(json.load(open('version.json'))['git_sha'])")
  HEAD_DATE=$(python3 -c "import json; print(json.load(open('version.json'))['build_date'])")
  log "  new seq=$NEW_SEQ version=$NEW_VERSION"
fi

# ── Step 3: 前端构建 ──────────────────────────────────────────
if [[ "$SKIP_FRONTEND" == "false" ]]; then
  log "[3/8] 前端构建 (npm ci && npm run build)..."
  [[ -d web/node_modules ]] || (cd web && npm ci)
  (cd web && npm run build)
  log "  web/dist/ 已生成 ✓"
else
  log "[3/8] 跳过前端构建 (--no-frontend)"
fi

# ── Step 4: Go 交叉编译 ───────────────────────────────────────
log "[4/8] Go 交叉编译 linux/amd64..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w \
    -X 'main.Version=$NEW_VERSION' \
    -X 'main.GitCommit=$HEAD_SHA' \
    -X 'main.BuildDate=$HEAD_DATE' \
    -X 'main.BuildNumber=$NEW_SEQ'" \
  -o "$BIN_NAME" \
  ./cmd/gateway
ls -lh "$BIN_NAME"
log "  编译完成 ✓"

if [[ "$DRY_RUN" == "true" ]]; then
  warn "[dry-run] 不上传, 退出"
  exit 0
fi

# ── Step 5: stop 服务 (避免 mmap 锁) ──────────────────────────
log "[5/8] stop 154 上的 $SERVICE_NAME..."
$SSH "$SSH_TARGET" "systemctl is-active --quiet $SERVICE_NAME && systemctl stop $SERVICE_NAME || true; sleep 2; systemctl is-active --quiet $SERVICE_NAME && echo STILL_ACTIVE || echo STOPPED"
log "  服务已停止 ✓"

# ── Step 6: scp 二进制 + web/dist + VERSION + .deploy_seq ─────
log "[6/8] scp 上传到 $REMOTE_DIR..."
$SSH "$SSH_TARGET" "mkdir -p $REMOTE_DIR/web"
$SCP "$BIN_NAME" "$SSH_TARGET:$REMOTE_DIR/$BIN_NAME"
# web/dist 作为目录上传 (scp -r src dst/ 时会创建 dst/src/)
if [[ -d web/dist ]]; then
  # 先清掉旧的 web/dist, 避免残留
  $SSH "$SSH_TARGET" "rm -rf $REMOTE_DIR/web"
  $SCP -r web/dist "$SSH_TARGET:$REMOTE_DIR/web/"
fi
echo "$NEW_VERSION" > /tmp/__deploy-154.version
echo "$NEW_SEQ"     > /tmp/__deploy-154.seq
$SCP /tmp/__deploy-154.version "$SSH_TARGET:$REMOTE_DIR/VERSION"
$SCP /tmp/__deploy-154.seq     "$SSH_TARGET:$REMOTE_DIR/.deploy_seq"
rm -f /tmp/__deploy-154.version /tmp/__deploy-154.seq
log "  文件已上传 ✓"

# ── Step 7: 写入 env-file + start ─────────────────────────────
log "[7/8] 写入 /etc/llm-gateway-go/env + systemd restart..."
$SSH "$SSH_TARGET" "mkdir -p /etc/llm-gateway-go /opt/llm-gateway-go/{data,logs,web}"
# 建 symlink: systemd unit 用 'llm-gateway-go', 实际二进制带版本号
$SSH "$SSH_TARGET" "ln -sf $REMOTE_DIR/$BIN_NAME $REMOTE_DIR/llm-gateway-go"
# 注意: env-file 由 ssh 端 heredoc 写入, 敏感值通过 SSHPASS 通道加密
# 我们用 base64 编码避免 heredoc 特殊字符问题
ENV_BODY=$(cat <<EOF
# /etc/llm-gateway-go/env — 154 server (47.97.111.154)
# 由 deploy-154.sh 自动写入 (mode 600)
# 修改请用 env-injector inject aliyun-gateway-154 + deploy-154-secrets.sh

# 服务端口
LLM_GATEWAY_PORT=8781
LLM_GATEWAY_HOST=0.0.0.0
LLM_GATEWAY_LOG_LEVEL=info
LLM_GATEWAY_LOG_FILE=$REMOTE_DIR/logs/gateway.log
ATTACHMENT_STORAGE_PATH=$REMOTE_DIR/data/attachments

# 数据库 (阿里云 252 上的 PG, 内网 172.16.2.210)
LLM_GATEWAY_DATABASE_URL=${LLM_GATEWAY_DATABASE_URL}

# 密钥 (从 env-injector 注入, 不在仓库)
LLM_GATEWAY_SECRET_KEY=${LLM_GATEWAY_SECRET_KEY}
LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY}

# Admin
LLM_GATEWAY_ADMIN_USER=${LLM_GATEWAY_ADMIN_USER:-admin}
LLM_GATEWAY_ADMIN_PASSWORD=${LLM_GATEWAY_ADMIN_PASSWORD:-}
LLM_GATEWAY_ADMIN_API_KEY=${LLM_GATEWAY_ADMIN_API_KEY:-$(openssl rand -hex 32 | sed 's/^/sk-/')}
EOF
)
ENV_B64=$(printf '%s' "$ENV_BODY" | base64 | tr -d '\n')
$SSH "$SSH_TARGET" "echo '$ENV_B64' | base64 -d > /etc/llm-gateway-go/env && chmod 600 /etc/llm-gateway-go/env && chown root:root /etc/llm-gateway-go/env"

# systemd unit (覆盖式, 幂等)
UNIT_BODY=$(cat <<EOF
[Unit]
Description=LLM Gateway Go (154)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$REMOTE_DIR
ExecStart=$REMOTE_DIR/llm-gateway-go
EnvironmentFile=/etc/llm-gateway-go/env
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
)
UNIT_B64=$(printf '%s' "$UNIT_BODY" | base64 | tr -d '\n')
$SSH "$SSH_TARGET" "echo '$UNIT_B64' | base64 -d > /etc/systemd/system/$SERVICE_NAME && chmod 644 /etc/systemd/system/$SERVICE_NAME"
$SSH "$SSH_TARGET" "systemctl daemon-reload && systemctl enable $SERVICE_NAME && systemctl restart $SERVICE_NAME && sleep 4 && systemctl is-active --quiet $SERVICE_NAME && echo ACTIVE || (echo NOT_ACTIVE; journalctl -u $SERVICE_NAME -n 50 --no-pager)"
log "  服务已启动 ✓"

# ── Step 8: smoke-verify ─────────────────────────────────────
log "[8/8] 验证..."
sleep 3
HEALTH=$($SSH "$SSH_TARGET" "curl -fsS http://localhost:8781/healthz || echo FAILED")
echo "  /healthz -> $HEALTH"
VERSION_RESP=$($SSH "$SSH_TARGET" "curl -fsS http://localhost:8781/api/system/version")
echo "  /api/system/version -> $VERSION_RESP"
REMOTE_VER=$($SSH "$SSH_TARGET" "cat $REMOTE_DIR/VERSION")
REMOTE_SEQ=$($SSH "$SSH_TARGET" "cat $REMOTE_DIR/.deploy_seq")
echo "  远程 VERSION = $REMOTE_VER"
echo "  远程 build_seq = $REMOTE_SEQ"

if [[ "$REMOTE_SEQ" != "$NEW_SEQ" ]]; then
  err "build_seq 不一致! 远程=$REMOTE_SEQ 期望=$NEW_SEQ"
  exit 1
fi

log "✅ 部署完成"
echo
echo "下一步:"
echo "  bash ~/.agents/skills/deploy-154/scripts/smoke-test.sh"