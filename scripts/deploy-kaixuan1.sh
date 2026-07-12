#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-kaixuan1.sh — 部署到 kaixuan-1 本地开发环境
# (重写于 2026-07-10 - 与 deploy-154 模式对齐)
#
# 用法:
#   ./scripts/deploy-kaixuan1.sh              # 完整部署流程
#   ./scripts/deploy-kaixuan1.sh --skip-build # 跳过编译（使用现有二进制）
#   ./scripts/deploy-kaixuan1.sh --skip-frontend # 跳过前端构建
#   ./scripts/deploy-kaixuan1.sh --skip-bump # 跳过版本号 bump
#   ./scripts/deploy-kaixuan1.sh --rollback   # 回滚到上一版本
#   ./scripts/deploy-kaixuan1.sh --dry-run    # 只显示计划
#
# 环境:
#   主机: kaixuan-1 (192.168.31.28, macOS ARM64)
#   DB: K3s 本地 PG (postgresql-0.pms-test.svc.cluster.local, 独立 schema)
#   Service: launchd (~/Library/LaunchAgents/com.kaixuan.llm-gateway-go.plist)
#   Listen port: 8080
#
# 前置 (HARD-GATE):
#   env-injector inject kaixuan-1
#   export SSHPASS='kaixuan123'
# =====================================================================

set -euo pipefail

# ── 颜色 ──────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}!${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}═══════ $* ═══════${N}"; }

# ── 配置 ──────────────────────────────────────────────────────────
SSH_HOST="192.168.31.28"
SSH_PORT="22"
SSH_USER="kaixuan"
SSH_PASS="${SSHPASS:-kaixuan123}"
REMOTE_DIR="~/workspace/official-deploy/services/llm-gateway-go"
REMOTE_ABS="/Users/kaixuan/workspace/official-deploy/services/llm-gateway-go"
LISTEN_PORT="${LLM_GATEWAY_PORT:-8080}"
SERVICE_LABEL="com.kaixuan.llm-gateway-go"
PLIST_REL="~/Library/LaunchAgents/${SERVICE_LABEL}.plist"
SCRIPTS_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPTS_DIR/.." && pwd)"

export SSHPASS="$SSH_PASS"

SKIP_BUILD=false
SKIP_FRONTEND=false
SKIP_BUMP=false
SKIP_DB_MIGRATION=false
ROLLBACK=false
DRY_RUN=false

for arg in "$@"; do
  case "$arg" in
    --skip-build)         SKIP_BUILD=true ;;
    --skip-frontend)      SKIP_FRONTEND=true ;;
    --skip-bump)          SKIP_BUMP=true ;;
    --skip-migration)     SKIP_DB_MIGRATION=true ;;
    --rollback)           ROLLBACK=true ;;
    --dry-run)            DRY_RUN=true ;;
    -h|--help)
      echo "用法: $0 [--skip-build] [--skip-frontend] [--skip-bump] [--skip-migration] [--rollback] [--dry-run]"
      exit 0 ;;
    *) err "未知参数: $arg"; exit 1 ;;
  esac
done

# ── 共享 SSH 重试 ─────────────────────────────────────────────────
source ~/.agents/skills/_lib/ssh-retry.sh
SSH_TARGET="${SSH_USER}@${SSH_HOST}"
export SSH_TARGET SSH_PORT

# ── 回滚模式 ──────────────────────────────────────────────────────
if $ROLLBACK; then
  bash ~/.agents/skills/deploy-kaixuan1/scripts/rollback.sh "$@"
  exit $?
fi

# ── 前置检查 ──────────────────────────────────────────────────────
phase "前置检查"

if ! command -v sshpass &> /dev/null; then
  err "缺少 sshpass 命令"; echo "安装: brew install sshpass"; exit 1
fi

info "检查 SSH 连通性..."
if ! remote "echo OK" &>/dev/null; then
  err "无法连接 $SSH_USER@$SSH_HOST:$SSH_PORT"
  exit 1
fi
ok "SSH 连接成功"

info "检查本地 Git 状态..."
if [ -n "$(git status --porcelain)" ]; then
  err "工作区有未提交的改动"
  echo "请先: git add . && git commit -m '...' && git push origin main"
  exit 1
fi
ok "Git 工作区干净"

COMMIT_SHA=$(git rev-parse --short HEAD)
info "当前 commit: $COMMIT_SHA"

# ── Step 1: bump-version ──────────────────────────────────────────
if ! $SKIP_BUMP; then
  phase "[1/8] bump-version (5 文件锁步)"
  bash "$SCRIPTS_DIR/bump-version.sh"
fi

NEW_SEQ=$(read_current_seq)
NEW_VERSION=$(read_current_version)
BIN_NAME="llm-gateway-go.v${NEW_SEQ}.darwin.arm64"
ok "  seq=$NEW_SEQ  version=$NEW_VERSION  bin=$BIN_NAME"

# ── Step 2: 前端构建 ─────────────────────────────────────────────
if ! $SKIP_FRONTEND; then
  phase "[2/8] 前端构建 (npm ci && npm run build)"
  if [[ ! -d web/node_modules ]]; then
    (cd web && npm ci)
  fi
  (cd web && npm run build)
  ok "  web/dist 已生成"
fi

# ── Step 3: macOS 本地编译 ───────────────────────────────────────
if ! $SKIP_BUILD; then
  phase "[3/8] macOS 本地编译 (darwin/arm64)"
  info "编译 darwin/arm64 二进制..."
  unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy
  go env -w GOPROXY=https://goproxy.cn,direct

  mkdir -p bin
  go build -trimpath \
    -ldflags="-s -w \
      -X 'main.Version=$NEW_VERSION' \
      -X 'main.GitCommit=$(git rev-parse --short=8 HEAD)' \
      -X 'main.BuildDate=$(date -u +%Y%m%d)' \
      -X 'main.BuildNumber=$NEW_SEQ'" \
    -o "bin/${BIN_NAME}" \
    ./cmd/gateway

  if [[ ! -f "bin/${BIN_NAME}" ]]; then
    err "编译失败"
    exit 1
  fi
  file "bin/${BIN_NAME}" | grep -q "Mach-O" || { err "二进制不是 Mach-O"; exit 1; }
  ls -lh "bin/${BIN_NAME}"
  ok "  编译完成"
fi

# ── Step 4: 验证 env-injector 注入（4-KEY 硬门禁） ────────────────
phase "[4/8] 4-KEY 硬门禁 (env-injector 已注入)"
for v in LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_ADMIN_API_KEY; do
  if [[ -z "${!v:-}" ]]; then
    err "环境变量 $v 未设置"
    err "请先执行: env-injector inject kaixuan-1"
    exit 2
  fi
done
ok "  4 个 LLM_GATEWAY_* KEY 已注入"

# ── Dry run 提前退出 ─────────────────────────────────────────────
if $DRY_RUN; then
  warn "[dry-run] 编译完成, 不上传"
  warn "  target = $SSH_USER@$SSH_HOST:$REMOTE_DIR"
  warn "  binary = bin/${BIN_NAME}"
  exit 0
fi

# ── Step 5: scp 到 kaixuan-1 ─────────────────────────────────────
phase "[5/8] scp 上传到 $SSH_HOST"
scp_put "bin/${BIN_NAME}" "${REMOTE_ABS}/bin/${BIN_NAME}.new"
remote "mkdir -p ${REMOTE_ABS}/logs ${REMOTE_ABS}/data ${REMOTE_ABS}/web ${REMOTE_ABS}/data/attachments"
remote "chmod +x ${REMOTE_ABS}/bin/${BIN_NAME}.new"
ok "  二进制已上传"

# 上传 web/dist（扁平化到 web/）
if [[ -d web/dist ]]; then
  info "上传 web/dist (扁平化到 web/)..."
  remote "rm -rf ${REMOTE_ABS}/web && mkdir -p ${REMOTE_ABS}/web"
  COPYFILE_DISABLE=1 tar czf - -C web dist | sshpass -e ssh -p "$SSH_PORT" \
    -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$HOME/.ssh/known_hosts_kaixuan1 \
    "$SSH_TARGET" "cat | tar xzf - -C ${REMOTE_ABS}/web --strip-components=1"
  ok "  web/ 已扁平化"
fi

# ── Step 6: .env.kaixuan-1 写入 ───────────────────────────────────
phase "[6/8] 写入 .env.kaixuan-1"

ENV_BODY=$(cat <<EOF
# .env.kaixuan-1 — 由 deploy-kaixuan1.sh 自动生成 (mode 600)
# 修改请用 env-injector inject kaixuan-1 + secrets-loader.sh

# 服务端口
LLM_GATEWAY_PORT=${LISTEN_PORT}
LLM_GATEWAY_HOST=0.0.0.0
LLM_GATEWAY_LOG_LEVEL=info
LLM_GATEWAY_LOG_FILE=${REMOTE_ABS}/logs/gateway.log
ATTACHMENT_STORAGE_PATH=${REMOTE_ABS}/data/attachments

# 数据库 (kaixuan-1 K3s 本地 PG, 独立 schema)
LLM_GATEWAY_DATABASE_URL=${LLM_GATEWAY_DATABASE_URL}

# 密钥 (从 env-injector 注入, 不在仓库)
LLM_GATEWAY_SECRET_KEY=${LLM_GATEWAY_SECRET_KEY}
LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY}

# Admin
LLM_GATEWAY_ADMIN_USER=${LLM_GATEWAY_ADMIN_USER:-admin}
LLM_GATEWAY_ADMIN_PASSWORD=${LLM_GATEWAY_ADMIN_PASSWORD:-}
LLM_GATEWAY_ADMIN_API_KEY=${LLM_GATEWAY_ADMIN_API_KEY}

# CORS
LLM_GATEWAY_CORS_ORIGINS=https://llm.itestu.cn
LLM_GATEWAY_STATIC_DIR=${REMOTE_ABS}/web
EOF
)

ENV_B64=$(printf '%s' "$ENV_BODY" | base64 | tr -d '\n')
remote "
  set -e
  ENV_FILE=${REMOTE_ABS}/.env.kaixuan-1
  B64='$ENV_B64'
  cp \$ENV_FILE \$ENV_FILE.bak.\$(date +%Y%m%d-%H%M%S) 2>/dev/null || true
  echo '$ENV_B64' | base64 -d > \$ENV_FILE
  chmod 600 \$ENV_FILE
  echo '✓ .env.kaixuan-1 updated'
"
ok "  .env.kaixuan-1 已更新"

# ── Step 7: launchd plist + atomic swap ───────────────────────────
phase "[7/8] launchd 集成 + atomic binary swap"

PLIST_BODY=$(cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${SERVICE_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${REMOTE_ABS}/bin/llm-gateway-go</string>
    </array>
    <key>WorkingDirectory</key>
    <string>${REMOTE_ABS}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin</string>
    </dict>
    <key>StandardOutPath</key>
    <string>${REMOTE_ABS}/logs/gateway.log</string>
    <key>StandardErrorPath</key>
    <string>${REMOTE_ABS}/logs/gateway.err.log</string>
    <key>KeepAlive</key>
    <true/>
    <key>RunAtLoad</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>SoftResourceLimits</key>
    <dict>
        <key>NumberOfFiles</key>
        <integer>65536</integer>
    </dict>
</dict>
</plist>
EOF
)
PLIST_B64=$(printf '%s' "$PLIST_BODY" | base64 | tr -d '\n')

remote "
  set -e
  PLIST_PATH=~/Library/LaunchAgents/${SERVICE_LABEL}.plist

  # 1. unload 当前 plist (如有)
  launchctl bootout gui/\$(id -u)/${SERVICE_LABEL} 2>/dev/null || true
  sleep 1

  # 2. atomic swap binary
  cd ${REMOTE_ABS}/bin
  if [ -f llm-gateway-go ]; then
    cp llm-gateway-go llm-gateway-go.v${NEW_SEQ}.darwin.arm64.bak.\$(date +%Y%m%d-%H%M%S)
    ls -t llm-gateway-go.v${NEW_SEQ}.darwin.arm64.bak.* 2>/dev/null | tail -n +6 | xargs -r rm -f
  fi
  mv ${BIN_NAME}.new llm-gateway-go
  chmod +x llm-gateway-go

  # 3. 写版本号
  printf '%s' '${NEW_SEQ}' > ${REMOTE_ABS}/.deploy_seq
  printf '%s' '${NEW_VERSION}' > ${REMOTE_ABS}/VERSION

  # 4. install plist
  mkdir -p ~/Library/LaunchAgents
  echo '${PLIST_B64}' | base64 -d > \$PLIST_PATH
  chmod 644 \$PLIST_PATH

  # 5. load
  launchctl bootstrap gui/\$(id -u) \$PLIST_PATH 2>/dev/null || launchctl load \$PLIST_PATH
  sleep 3

  # 6. verify
  if launchctl list | grep -q ${SERVICE_LABEL}; then
    echo '✓ launchd ACTIVE'
  else
    echo '✗ launchd NOT ACTIVE'
    tail -30 ${REMOTE_ABS}/logs/gateway.err.log 2>/dev/null || true
    exit 1
  fi
"
ok "  服务已启动"

# ── Step 8: smoke-verify ──────────────────────────────────────────
phase "[8/8] 验证部署"

sleep 3
HEALTH=$(remote "curl -fsS http://127.0.0.1:${LISTEN_PORT}/healthz" 2>&1 || echo "FAILED")
echo "  /healthz -> $HEALTH"

REMOTE_VER=$(remote "cat ${REMOTE_ABS}/VERSION" | tr -d '[:space:]')
REMOTE_SEQ=$(remote "cat ${REMOTE_ABS}/.deploy_seq" | tr -d '[:space:]')
echo "  远程 VERSION = $REMOTE_VER"
echo "  远程 build_seq = $REMOTE_SEQ"

if [[ "$REMOTE_SEQ" != "$NEW_SEQ" ]]; then
  err "build_seq 不一致! 远程=$REMOTE_SEQ 期望=$NEW_SEQ"
  exit 1
fi

ok "✅ 部署完成"
echo
echo "下一步:"
echo "  1. 查看日志: ssh $SSH_USER@$SSH_HOST 'tail -f ${REMOTE_DIR}/logs/gateway.log'"
echo "  2. NPS 隧道: 252 nps web 加 11008→127.0.0.1:${LISTEN_PORT}"
echo "  3. 252 nginx: location / proxy_pass http://127.0.0.1:11008"
echo "  4. 公网验证: curl -fsS https://llm.itestu.cn/healthz"
echo "  5. 如果有问题: $0 --rollback"
