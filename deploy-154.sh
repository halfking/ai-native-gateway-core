#!/usr/bin/env bash
# =====================================================================
# deploy-154.sh — 部署到 154 生产环境
#
# 用法:
#   ./deploy-154.sh              # 完整部署流程
#   ./deploy-154.sh --skip-build # 跳过编译（使用现有二进制）
#   ./deploy-154.sh --rollback   # 回滚到上一版本
#
# 环境:
#   主机: 154 (47.97.111.154 / llm.kxpms.cn)
#   数据库: PostgreSQL on 252 (生产库)
#   系统: Linux AMD64
#
# ⚠️ 注意: 这是生产环境，操作需谨慎！
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
SSH_HOST="47.97.111.154"
SSH_PORT="25022"
SSH_USER="root"
SSH_PASS="Kaixuan2026&#*9527"
REMOTE_DIR="/opt/llm-gateway-go"
BINARY_NAME="gateway"  # 注意：154 上是 gateway，不是 llm-gateway-go

export SSHPASS="$SSH_PASS"

SKIP_BUILD=false
ROLLBACK=false

for arg in "$@"; do
  case "$arg" in
    --skip-build) SKIP_BUILD=true ;;
    --rollback)   ROLLBACK=true ;;
    -h|--help)
      echo "用法: $0 [--skip-build] [--rollback]"
      echo ""
      echo "选项:"
      echo "  --skip-build  跳过本地编译，使用现有二进制"
      echo "  --rollback    回滚到上一版本"
      echo ""
      echo "⚠️  这是生产环境 (llm.kxpms.cn)，操作需谨慎！"
      exit 0
      ;;
    *)
      err "未知参数: $arg"
      exit 1
      ;;
  esac
done

# ── 回滚模式 ──────────────────────────────────────────────────────
if $ROLLBACK; then
  warn "⚠️  开始回滚生产环境 154"
  read -p "确认回滚？(yes/no): " CONFIRM
  if [ "$CONFIRM" != "yes" ]; then
    echo "已取消"
    exit 0
  fi
  
  phase "回滚 154"
  
  info "停止服务..."
  sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    systemctl stop llm-gateway 2>/dev/null || \
    supervisorctl stop llm-gateway 2>/dev/null || \
    pkill -f '$BINARY_NAME' || true
    sleep 2
  "
  
  info "恢复备份..."
  sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    cd $REMOTE_DIR
    BACKUP=\$(ls -t ${BINARY_NAME}.bak.* 2>/dev/null | head -1)
    if [ -z \"\$BACKUP\" ]; then
      echo '✗ 未找到备份文件'
      exit 1
    fi
    echo \"恢复备份: \$BACKUP\"
    cp \"\$BACKUP\" $BINARY_NAME
    chmod +x $BINARY_NAME
  "
  
  info "启动服务..."
  sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    systemctl start llm-gateway 2>/dev/null || \
    supervisorctl start llm-gateway 2>/dev/null || \
    (cd $REMOTE_DIR && nohup ./$BINARY_NAME > logs/gateway.log 2>&1 &)
    sleep 5
  "
  
  info "验证服务..."
  sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    systemctl status llm-gateway --no-pager 2>/dev/null || \
    supervisorctl status llm-gateway 2>/dev/null || \
    ps aux | grep '$BINARY_NAME' | grep -v grep
  "
  
  ok "回滚完成"
  echo ""
  echo "验证: curl -s https://llm.kxpms.cn/health"
  exit 0
fi

# ── 前置检查 ──────────────────────────────────────────────────────
phase "前置检查"

warn "⚠️  即将部署到生产环境 154 (llm.kxpms.cn)"
read -p "确认继续？(yes/no): " CONFIRM
if [ "$CONFIRM" != "yes" ]; then
  echo "已取消"
  exit 0
fi

if ! command -v sshpass &> /dev/null; then
  err "缺少 sshpass 命令"
  echo "安装: brew install sshpass"
  exit 1
fi

info "检查 SSH 连通性..."
if ! sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no -o ConnectTimeout=10 "$SSH_USER@$SSH_HOST" "echo 'OK'" &>/dev/null; then
  err "无法连接到 $SSH_HOST:$SSH_PORT"
  exit 1
fi
ok "SSH 连接成功"

info "检查本地 Git 状态..."
if [ -n "$(git status --porcelain)" ]; then
  err "工作区有未提交的改动"
  echo "请先提交: git add . && git commit -m '...' && git push origin main"
  exit 1
fi
ok "Git 工作区干净"

COMMIT_SHA=$(git rev-parse --short HEAD)
COMMIT_MSG=$(git log -1 --pretty=%B | head -1)
info "当前 commit: $COMMIT_SHA — $COMMIT_MSG"

# ── 本地交叉编译 ──────────────────────────────────────────────────
if ! $SKIP_BUILD; then
  phase "本地交叉编译 (Linux AMD64)"
  
  info "禁用代理，使用国内镜像..."
  unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy
  go env -w GOPROXY=https://goproxy.cn,direct
  
  info "编译 Linux AMD64 二进制..."
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/${BINARY_NAME}-linux-amd64 ./cmd/gateway
  
  if [ ! -f "bin/${BINARY_NAME}-linux-amd64" ]; then
    err "编译失败"
    exit 1
  fi
  
  info "验证二进制..."
  ls -lh "bin/${BINARY_NAME}-linux-amd64"
  file "bin/${BINARY_NAME}-linux-amd64" | grep -q "Linux" || {
    err "二进制不是 Linux 格式"
    exit 1
  }
  
  ok "编译完成"
else
  info "跳过编译（--skip-build）"
  if [ ! -f "bin/${BINARY_NAME}-linux-amd64" ]; then
    err "找不到 bin/${BINARY_NAME}-linux-amd64"
    echo "请先编译或去掉 --skip-build"
    exit 1
  fi
fi

# ── 上传二进制 ──────────────────────────────────────────────────
phase "上传二进制到 154"

info "上传 ${BINARY_NAME}-linux-amd64..."
sshpass -e scp -P "$SSH_PORT" -o StrictHostKeyChecking=no \
  "bin/${BINARY_NAME}-linux-amd64" \
  "$SSH_USER@$SSH_HOST:$REMOTE_DIR/${BINARY_NAME}.new"

ok "二进制已上传"

# ── 备份并替换 ──────────────────────────────────────────────────
phase "备份旧版本并替换"

sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  cd $REMOTE_DIR
  
  # 备份
  if [ -f $BINARY_NAME ]; then
    BACKUP_NAME=${BINARY_NAME}.bak.\$(date +%Y%m%d_%H%M%S)
    cp $BINARY_NAME \"\$BACKUP_NAME\"
    echo \"✓ 备份完成: \$BACKUP_NAME\"
  else
    echo '⚠ 未找到旧版本'
  fi
  
  # 替换
  mv ${BINARY_NAME}.new $BINARY_NAME
  chmod +x $BINARY_NAME
  echo '✓ 二进制已替换'
"

ok "替换完成"

# ── 重启服务 ──────────────────────────────────────────────────────
phase "重启服务"

info "尝试通过 systemd 重启..."
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  if command -v systemctl &> /dev/null; then
    echo '▶ systemctl restart llm-gateway'
    systemctl restart llm-gateway
    sleep 5
    systemctl status llm-gateway --no-pager
  elif command -v supervisorctl &> /dev/null; then
    echo '▶ supervisorctl restart llm-gateway'
    supervisorctl restart llm-gateway
    sleep 5
    supervisorctl status llm-gateway
  else
    echo '⚠ 未检测到服务管理器'
    echo '手动重启...'
    pkill -f '$BINARY_NAME' || true
    sleep 2
    cd $REMOTE_DIR && nohup ./$BINARY_NAME > logs/gateway.log 2>&1 &
    sleep 5
    ps aux | grep '$BINARY_NAME' | grep -v grep
  fi
"

ok "服务已重启"

# ── 验证部署 ──────────────────────────────────────────────────
phase "验证部署"

info "检查进程..."
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  ps aux | grep '$BINARY_NAME' | grep -v grep || echo '✗ 进程未找到'
"

info "查看最新日志..."
sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  tail -30 $REMOTE_DIR/logs/gateway.log 2>/dev/null || journalctl -u llm-gateway --no-pager -n 30
"

info "测试健康检查..."
sleep 5
if curl -s -f https://llm.kxpms.cn/health > /dev/null 2>&1; then
  ok "健康检查通过"
else
  warn "健康检查失败，请排查"
fi

# ── 完成 ──────────────────────────────────────────────────────
echo ""
ok "部署完成！"
echo ""
echo "下一步:"
echo "  1. 查看实时日志: ssh -p $SSH_PORT $SSH_USER@$SSH_HOST 'journalctl -u llm-gateway -f'"
echo "  2. 测试 API: curl -s https://llm.kxpms.cn/v1/models"
echo "  3. 监控 Circuit Breaker: ssh ... 'grep -E \"circuit|session_blacklist\" $REMOTE_DIR/logs/gateway.log'"
echo "  4. 如果有问题，立即回滚: ./deploy-154.sh --rollback"
echo ""
warn "⚠️  请持续监控 30 分钟，确保服务稳定"
