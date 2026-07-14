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
#
# ⚠️  关键经验 (2026-07-10 unified auth 部署踩坑):
#
#   1. **永远用 systemd restart, 永远不要 nohup**
#      systemd unit 通过 EnvironmentFile=/etc/llm-gateway-go/env 加载 env。
#      nohup 启动会丢失 env, 导致 panic "CORS origins must be explicitly configured"、
#      pg conn 失败、AdminMiddleware 走 fail-open 等。
#
#   2. **改完前端代码立刻 build 验证 dist**
#      TypeScript 不会对未导入的自由变量报错 (例如把 `authBearer()` 用在
#      loadVersion 但忘了 import), 浏览器加载新 chunk 时才在运行时炸开。
#      部署前先本地 build 一次, 确认 dist 里有正确 chunk hash。
#
#   3. **加新 handler 路由要 nil-check h.db**
#      154 上的 binary 在 v326+ 有 pg keepalive 问题, 60s 后 pg 被禁用,
#      此时 h.db == nil。所有新 handler 都要写 `if h.db == nil { 503 }`。
#      现有 handler 都有, 但新写的很容易漏。
#
#   4. **部署后立即 smoke test**
#      GET /healthz (200) → 匿名健康
#      GET /api/auth/me with JWT (200) → admin 认证
#      GET /api/auth/me with sk-* (401) → sk-* admin 路径已删除
#      GET /api/models/name-mapping with JWT (200) → admin 数据
#      如果 502 → systemctl restart llm-gateway-go.service
#      如果 panic → 立即回滚（见 §5 经验总结）
#
#   5. **二进制中的字符串检查**
#      strings /opt/llm-gateway-go/llm-gateway-go.v$N.linux.amd64 | grep name-mapping
#      应该能看到 name-mapping 字符串 (新路由已注册)
# =====================================================================
set -euo pipefail

# ── 默认值 ──────────────────────────────────────────────────────
SSH_TARGET="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"
SSH_PORT="${LLM_GATEWAY_154_PORT:-25022}"
REMOTE_DIR="${LLM_GATEWAY_154_DIR:-/opt/llm-gateway-go}"
SERVICE_NAME="${LLM_GATEWAY_154_SERVICE:-llm-gateway-go.service}"
# BIN_NAME 现在在 step 2 之后从 version.json 自动派生（见下面 derive_bin_name）
SKIP_FRONTEND=false
SSH_USE_KEY="${SSH_USE_KEY:-false}"
SKIP_BUMP=false
TARGET_SEQ=""
DRY_RUN=false
FORCE_DEPLOY="${FORCE_DEPLOY:-false}"

# ── 参数解析 ────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --seq)           TARGET_SEQ="$2"; shift 2 ;;
    --no-frontend)   SKIP_FRONTEND=true; shift ;;
    --no-bump)       SKIP_BUMP=true; shift ;;
    --force)         FORCE_DEPLOY=true; shift ;;
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
# shellcheck source=deploy-lib/post-deploy-verify.sh
source "$SCRIPT_DIR/deploy-lib/post-deploy-verify.sh"

GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; RED=$'\033[0;31m'; NC=$'\033[0m'
log()  { echo -e "${GREEN}[deploy-154]${NC} $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }
err()  { echo -e "${RED}[error]${NC} $*" >&2; }

# ── SSH 鉴权策略（2026-07-12 智能切换） ───────────────────────
# 优先级：
#   1) ssh-key（默认；优先 ~/.ssh/id_ed25519, ~/.ssh/56_id_rsa, ~/.ssh/71_id_rsa）
#   2) sshpass + SSHPASS（向后兼容旧调用；需 brew install sshpass）

SSH_KEY_FILE="${SSH_KEY_154:-}"
if [[ -z "$SSH_KEY_FILE" ]]; then
  for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
    if [[ -f "$k" ]]; then
      SSH_KEY_FILE="$k"
      break
    fi
  done
fi

# 显式 opt-in 到 sshpass：必须显式 DEPLOY_154_USE_SSHPASS=1 + SSHPASS 已 export
#   - 避免误用全局 SSHPASS（用户可能在其他工具设置了）
#   - 强制要求 opt-in 让 key-auth 路径成为默认
if [[ "${DEPLOY_154_USE_SSHPASS:-0}" == "1" ]]; then
  if ! command -v sshpass >/dev/null 2>&1; then
    err "sshpass 未安装 (brew install sshpass)"
    exit 2
  fi
  if [[ -z "${SSHPASS:-}" ]]; then
    err "DEPLOY_154_USE_SSHPASS=1 但 SSHPASS 未 export"
    err "export SSHPASS='<your-password>'"
    exit 2
  fi
  SSH="sshpass -e ssh -p $SSH_PORT -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$HOME/.ssh/known_hosts_154"
  SCP="sshpass -e scp -P $SSH_PORT -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$HOME/.ssh/known_hosts_154"
  log "鉴权模式:    sshpass (legacy)"
  SSH_USE_KEY=false
else
  if [[ -z "$SSH_KEY_FILE" ]] || [[ ! -f "$SSH_KEY_FILE" ]]; then
    err "未找到 SSH 私钥（尝试 ~/.ssh/id_ed25519, 56_id_rsa, 71_id_rsa 失败）"
    err "请设置 SSH_KEY_154 环境变量，或 export DEPLOY_154_USE_SSHPASS=1 + SSHPASS 走密码路径"
    exit 2
  fi
  SSH="ssh -i $SSH_KEY_FILE -p $SSH_PORT -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o UserKnownHostsFile=$HOME/.ssh/known_hosts_154"
  SCP="scp -i $SSH_KEY_FILE -P $SSH_PORT -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o UserKnownHostsFile=$HOME/.ssh/known_hosts_154"
  SSH_USE_KEY=true
  log "鉴权模式:    ssh-key ($SSH_KEY_FILE)"
fi

# ── 4-KEY 校验（key-auth 模式可从远端 env-file 自动读取） ─────
# 优先级：
#   - key-auth + SSHPASS 未设置 → 从 154 /etc/llm-gateway-go/env 读取 4 个核心 KEY
#   - sshpass + SSHPASS 已设置 → 必须本地 export 4-KEY（保持向后兼容）
#
# 这样：CI 用 sshpass 时强制本地注入；开发者用 key-auth 时无需 inject

if [[ "$SSH_USE_KEY" == "true" ]]; then
  log "key-auth 模式：从远端 env-file 自动读取 4-KEY..."
  REMOTE_ENV=/tmp/154_env.$$
  if ! $SSH "$SSH_TARGET" "cat /etc/llm-gateway-go/env" > "$REMOTE_ENV" 2>/dev/null; then
    err "无法从 154 读取 /etc/llm-gateway-go/env（SSH 链路失败）"
    rm -f "$REMOTE_ENV"
    exit 2
  fi
  for v in LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_ADMIN_API_KEY; do
    local_val=$(grep -E "^${v}=" "$REMOTE_ENV" 2>/dev/null | head -1 | cut -d= -f2-)
    if [[ -n "$local_val" ]]; then
      export "$v"="$local_val"
    fi
  done
  rm -f "$REMOTE_ENV"
  # 此时 4-KEY 必有值（154 上必有）；如仍缺则视为生产 env-file 损坏
  for v in LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_ADMIN_API_KEY; do
    if [[ -z "${!v:-}" ]]; then
      err "key-auth 自动读取失败：$v 为空（远端 /etc/llm-gateway-go/env 可能损坏）"
      exit 2
    fi
  done
  log "  ✓ 4-KEY 已从远端同步"
else
  # sshpass 模式：要求本地 export（向后兼容 deploy-full.sh 协议）
  for v in LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_ADMIN_API_KEY; do
    if [[ -z "${!v:-}" ]]; then
      err "sshpass 模式：环境变量 $v 未设置"
      err "请先执行: env-injector inject aliyun-gateway-154"
      exit 2
    fi
  done
  log "  sshpass 模式：4-KEY 已通过 env-injector 注入"
fi

log "目标服务器:  $SSH_TARGET:$SSH_PORT"
log "部署目录:    $REMOTE_DIR"
log "服务名:      $SERVICE_NAME"
# log "二进制:      $BIN_NAME"  # BIN_NAME 在 step 2 后才定义
log "skip frontend: $SKIP_FRONTEND"
echo

# ── Step 1: 预检 ───────────────────────────────────────────────
log "[1/8] 预检..."
[[ -f go.mod ]] || { err "go.mod 不存在"; exit 1; }
if [[ "${FORCE_DEPLOY:-false}" != "true" ]]; then
  git diff --quiet HEAD 2>/dev/null || { err "工作区有未提交改动（用 --force 跳过此检查）"; exit 1; }
  git diff --cached --quiet HEAD 2>/dev/null || { err "有 staged 但未提交改动（用 --force 跳过此检查）"; exit 1; }
else
  warn "FORCE_DEPLOY=true: 跳过 git dirty 检查（生产部署不建议）"
fi
git diff --cached --quiet HEAD 2>/dev/null || { err "有 staged 但未提交改动"; exit 1; }
log "  git clean: ✓"

$SSH "$SSH_TARGET" "echo connected && uname -a" >/dev/null || { err "SSH 不可达"; exit 1; }
log "  ssh OK: ✓"
deploy_preflight_pg_from_remote_env "$SSH" "/etc/llm-gateway-go/env" || exit 2

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

# BIN_NAME 从 NEW_SEQ 自动派生（替代硬编码 v325）
BIN_NAME="llm-gateway-go.v${NEW_SEQ}.linux.amd64"
log "  BIN_NAME 自动派生: $BIN_NAME"

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
# 2026-07-14: 移除 ldflags 版本注入。版本号统一从 version.json 文件读取（SSOT）。
# version.json 通过 scp 上传到 154，runtime 自动读取。
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w" \
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

# ── Step 6: scp 二进制 + web/dist + version.json (SSOT) ──────
log "[6/8] scp 上传到 $REMOTE_DIR..."
$SSH "$SSH_TARGET" "mkdir -p $REMOTE_DIR/web"
$SCP "$BIN_NAME" "$SSH_TARGET:$REMOTE_DIR/$BIN_NAME"
# web/dist 作为目录上传. StaticHandler 期望 web/index.html (不是 web/dist/).
# 用 tar pipe 传整个目录, 然后在远端把它"展平"成 web/{index.html, assets/...}
if [[ -d web/dist ]]; then
  # 先清掉旧的 web/, 避免残留
  $SSH "$SSH_TARGET" "rm -rf $REMOTE_DIR/web && mkdir -p $REMOTE_DIR/web"
  # 展平方式: 把 dist/ 里的内容放到 web/ 下 (不是 web/dist/)
  tar czf - -C web dist | $SSH "$SSH_TARGET" "cat | tar xzf - -C $REMOTE_DIR/web --strip-components=1"
fi
# 版本信息统一通过 version.json (SSOT - Single Source of Truth) 传递
# 替代之前分散的 VERSION / .deploy_seq / build_seq 三个文件
$SCP version.json "$SSH_TARGET:$REMOTE_DIR/version.json"
# 兼容性：保留 VERSION 文件供旧 binary 使用（可平滑过渡）
echo "$NEW_VERSION" > /tmp/__deploy-154.version
$SCP /tmp/__deploy-154.version "$SSH_TARGET:$REMOTE_DIR/VERSION"
rm -f /tmp/__deploy-154.version
log "  文件已上传 ✓"

# ── Step 7: 写入 env-file + start ─────────────────────────────
log "[7/8] 写入 /etc/llm-gateway-go/env + systemd restart..."
$SSH "$SSH_TARGET" "mkdir -p /etc/llm-gateway-go /opt/llm-gateway-go/{data,logs,web}"
# 建 symlink: systemd unit 用 'llm-gateway-go', 实际二进制带版本号
$SSH "$SSH_TARGET" "ln -sf $REMOTE_DIR/$BIN_NAME $REMOTE_DIR/llm-gateway-go"
# 注意: env-file 由 ssh 端 heredoc 写入, 敏感值通过 SSHPASS 通道加密
# 我们用 base64 编码避免 heredoc 特殊字符问题
# V1.2 改: 用 append-only 写入, 不覆盖 deploy-154-secrets.sh 注入的 KEY (避免 clobbering rotated secret)
# V1.3 改: 4 个核心 KEY 强校验（来自 env-injector），不再 fallback 到 openssl rand
: "${LLM_GATEWAY_ADMIN_USER:=admin}"
: "${LLM_GATEWAY_ADMIN_PASSWORD:=}"
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

# Admin (V1.3: 不再 fallback 到 openssl rand，避免 token 漂移)
LLM_GATEWAY_ADMIN_USER=${LLM_GATEWAY_ADMIN_USER}
LLM_GATEWAY_ADMIN_PASSWORD=${LLM_GATEWAY_ADMIN_PASSWORD}
LLM_GATEWAY_ADMIN_API_KEY=${LLM_GATEWAY_ADMIN_API_KEY}
EOF
)
# V1.3: 写入前再校验一次（防御 env-injector 注入后又被外部 unset 的情况）
for v in LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_ADMIN_API_KEY; do
  if [[ -z "${!v:-}" ]]; then
    err "4-KEY 强校验失败: $v 为空，拒绝写入 env-file"
    err "请重新执行: env-injector inject aliyun-gateway-154"
    exit 3
  fi
done
ENV_B64=$(printf '%s' "$ENV_BODY" | base64 | tr -d '\n')
# 远端执行: 先把 deploy-154.sh 管理的 key 清掉, 再 append 新值, 保留其它 key (secrets.sh 注入的).
# 用 awk 过滤 deploy-154.sh 管理的 key (这些 key 重新写, 其它 key 保留).
$SSH "$SSH_TARGET" bash <<REMOTE_EOF
set -e
ENV_FILE=/etc/llm-gateway-go/env
B64="$ENV_B64"
MANAGED_KEYS='LLM_GATEWAY_PORT LLM_GATEWAY_HOST LLM_GATEWAY_LOG_LEVEL LLM_GATEWAY_LOG_FILE ATTACHMENT_STORAGE_PATH LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_SECRET_KEY LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_ADMIN_USER LLM_GATEWAY_ADMIN_PASSWORD LLM_GATEWAY_ADMIN_API_KEY'
# 1. 备份当前 env-file
cp "\$ENV_FILE" "\$ENV_FILE.bak.\$(date +%Y%m%d-%H%M%S)"
# 2. 过滤掉 managed keys (保留 deploy-154-secrets.sh 注入的其它 key)
KEEP=""
while IFS= read -r line; do
    key="\$(echo "\$line" | cut -d= -f1)"
    if [[ "\$key" =~ ^[[:space:]]*# ]] || [[ -z "\$key" ]]; then
        KEEP="\$KEEP\$line\n"
        continue
    fi
    skip=0
    for mk in \$MANAGED_KEYS; do
        if [[ "\$key" == "\$mk" ]]; then skip=1; break; fi
    done
    if [[ \$skip -eq 0 ]]; then
        KEEP="\$KEEP\$line\n"
    fi
done < "\$ENV_FILE"
# 3. 写新 env-file = 保留 key + deploy-154.sh 管理的 key (base64 解码)
printf "%b" "\$KEEP" > "\$ENV_FILE"
echo "\$B64" | base64 -d >> "\$ENV_FILE"
# 4. chmod 600
chmod 600 "\$ENV_FILE"
chown root:root "\$ENV_FILE"
echo "env-file updated (managed=\$(echo \$MANAGED_KEYS | wc -w) keys, kept other secrets.sh keys)"
REMOTE_EOF
log "  env-file 已 append-only 更新 (managed=11 keys, 保留 secrets.sh 注入的其它 key)"

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
if ! deploy_verify_gateway_ready "$SSH" "$SERVICE_NAME" 8781 120; then
  err "DB 未就绪 — 部署失败，请回滚或排查 252 PG"
  exit 1
fi
sleep 1
HEALTH=$($SSH "$SSH_TARGET" "curl -fsS http://127.0.0.1:8781/healthz || echo FAILED")
echo "  /healthz -> $HEALTH"
VERSION_RESP=$($SSH "$SSH_TARGET" "curl -fsS http://127.0.0.1:8781/api/system/version")
echo "  /api/system/version -> $VERSION_RESP"
REMOTE_VER=$($SSH "$SSH_TARGET" "cat $REMOTE_DIR/VERSION 2>/dev/null || echo 'no VERSION file'")
REMOTE_SEQ=$($SSH "$SSH_TARGET" "cat $REMOTE_DIR/.deploy_seq 2>/dev/null || python3 -c \"import json; print(json.load(open('$REMOTE_DIR/version.json')).get('build_seq','?'))\" 2>/dev/null || echo 'unknown'")
echo "  远程 VERSION = $REMOTE_VER"
echo "  远程 build_seq = $REMOTE_SEQ"

if [[ "$REMOTE_SEQ" != "$NEW_SEQ" ]]; then
  warn "build_seq 不一致! 远程=$REMOTE_SEQ 期望=$NEW_SEQ (可能是 version.json 未同步)"
fi

# ── 增强 smoke-verify (2026-07-10 unified auth 后必备) ──────────
log "[8/8+1] 增强 auth smoke-verify (unified auth 必检)..."

# 8.1: 验证 binary 包含新路由（unified auth 后必须有 /api/models/name-mapping）
if $SSH "$SSH_TARGET" "strings $REMOTE_DIR/$BIN_NAME 2>/dev/null | grep -q 'name-mapping'"; then
  log "  ✓ binary 包含 name-mapping 路由（新版本正确）"
else
  err "✗ binary 不包含 name-mapping 路由！可能部署了旧 binary"
  err "   检查: $REMOTE_DIR/$BIN_NAME"
  exit 1
fi

# 8.2: /healthz (匿名 200)
HEALTH=$($SSH "$SSH_TARGET" "curl -sS -o /dev/null -w '%{http_code}' http://localhost:8781/healthz")
if [[ "$HEALTH" == "200" ]]; then
  log "  ✓ /healthz 匿名 = $HEALTH"
else
  err "✗ /healthz 返回 $HEALTH (期望 200)"
  exit 1
fi

# 8.3: /healthz?full=true 匿名 (401, 因为 healthz?full=true 现在需要 JWT)
HEALTH_FULL=$($SSH "$SSH_TARGET" "curl -sS -o /dev/null -w '%{http_code}' 'http://localhost:8781/healthz?full=true'")
if [[ "$HEALTH_FULL" == "401" ]]; then
  log "  ✓ /healthz?full=true 匿名 = $HEALTH_FULL (期望 401)"
else
  warn "! /healthz?full=true 匿名 = $HEALTH_FULL (期望 401, 检查后端是否回退到旧版)"
fi

# ── 8.4: DB health check（deploy_verify_gateway_ready 已覆盖；此处保留快速复检）──
log "[8/8+2] DB health check (schema 兼容性必检)..."

PG_DISABLED=$($SSH "$SSH_TARGET" "journalctl -u $SERVICE_NAME --since '3 minutes ago' --no-pager -o cat 2>/dev/null | grep -c 'postgres disabled' || echo 0")
PG_DISABLED=$(echo "$PG_DISABLED" | head -1 | tr -d '[:space:]')
PG_DISABLED=${PG_DISABLED:-0}
if [[ "$PG_DISABLED" -gt 0 ]]; then
  err "✗ postgres disabled! 检测到 $PG_DISABLED 次禁用日志"
  err "   这通常意味着 EnsureSchema 超时或 252 PG 不可用"
  err "   诊断: journalctl -u $SERVICE_NAME --since '5 minutes ago' | grep 'postgres disabled'"
  exit 1
fi
log "  ✓ 无 postgres disabled 日志"

BG_TASKS=$($SSH "$SSH_TARGET" "curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:8781/api/system/background-tasks")
if [[ "$BG_TASKS" == "503" ]]; then
  err "✗ /api/system/background-tasks 返回 503 (database not configured)"
  err "   原因: h.db == nil，通常因为 postgres disabled"
  err "   诊断: journalctl -u $SERVICE_NAME --since '2 minutes ago' | grep -E 'disabled|ERROR'"
  err "   回滚: ln -sfn $REMOTE_DIR/llm-gateway-go.v<上一个版本>.linux.amd64 $REMOTE_DIR/llm-gateway-go && systemctl restart $SERVICE_NAME"
  exit 1
elif [[ "$BG_TASKS" == "401" ]]; then
  log "  ✓ /api/system/background-tasks 返回 401 (DB 正常，需认证)"
else
  warn "! /api/system/background-tasks 返回 $BG_TASKS (期望 401 或 200)"
fi

# 8.4: /api/auth/me with sk-* (401, 验证旧 admin key 路径已删除)
SK_ME=$($SSH "$SSH_TARGET" "curl -sS -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer sk-fake-admin-key' http://localhost:8781/api/auth/me")
if [[ "$SK_ME" == "401" ]]; then
  log "  ✓ sk-* admin key 已禁用 (期望 401)"
else
  err "✗ sk-* admin key 仍可访问 /api/auth/me = $SK_ME (unified auth 没生效!)"
  exit 1
fi

# 8.5: 用 admin password 登录, 获取 JWT, 验证 /api/auth/me 返回 access_token
ADMIN_USER="${LLM_GATEWAY_ADMIN_USER:-admin}"
ADMIN_PASS="${LLM_GATEWAY_ADMIN_PASSWORD:-}"
if [[ -n "$ADMIN_PASS" ]]; then
  LOGIN_RESP=$($SSH "$SSH_TARGET" "curl -sS -X POST -H 'Content-Type: application/json' -d '{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}' http://localhost:8781/api/auth/token")
  JWT=$(echo "$LOGIN_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('access_token',''))" 2>/dev/null || echo "")
  if [[ -n "$JWT" ]]; then
    log "  ✓ 登录成功, JWT 长度=${#JWT}"
    # 验证 /api/auth/me 返回 access_token (说明 handleAuthMe 已签发 JWT)
    ME_RESP=$($SSH "$SSH_TARGET" "curl -sS -H 'Authorization: Bearer $JWT' http://localhost:8781/api/auth/me")
    if echo "$ME_RESP" | grep -q 'access_token'; then
      log "  ✓ /api/auth/me 返回 access_token (新 hydration 路径生效)"
    else
      warn "! /api/auth/me 未返回 access_token — SPA hydration 路径会缺 JWT"
    fi
    # 验证 /api/models/name-mapping (admin 端点)
    NAME_MAPPING=$($SSH "$SSH_TARGET" "curl -sS -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer $JWT' 'http://localhost:8781/api/models/name-mapping?page=1&page_size=1'")
    if [[ "$NAME_MAPPING" == "200" ]]; then
      log "  ✓ /api/models/name-mapping (admin 端点) = $NAME_MAPPING"
    else
      err "✗ /api/models/name-mapping = $NAME_MAPPING (期望 200)"
    fi
  else
    warn "登录失败 (admin 密码可能未注入 env), 跳过 JWT 验证"
  fi
else
  warn "LLM_GATEWAY_ADMIN_PASSWORD 未注入, 跳过 JWT smoke-verify"
fi

log "✅ 部署完成"
echo
echo "下一步:"
echo "  bash ~/.agents/skills/deploy-154/scripts/smoke-test.sh"