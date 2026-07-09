#!/usr/bin/env bash
# =====================================================================
# deploy-kaixuan1.sh — 部署到 kaixuan-1 本地测试环境
#
# 用法:
#   ./deploy-kaixuan1.sh              # 完整部署流程
#   ./deploy-kaixuan1.sh --skip-build # 跳过编译（使用现有二进制）
#   ./deploy-kaixuan1.sh --rollback   # 回滚到上一版本
#
# 环境:
#   主机: kaixuan-1 (192.168.31.28)
#   数据库: PostgreSQL on kaixuan-1 (本地测试库)
#   系统: macOS ARM64
# =====================================================================

set -euo pipefail

# ── 颜色 ──────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}═══════ $* ═══════${N}"; }

# ── 配置 ──────────────────────────────────────────────────────────
SSH_HOST="192.168.31.28"
SSH_USER="kaixuan"
SSH_PASS="kaixuan123"
REMOTE_DIR="~/workspace/official-deploy/services/llm-gateway-go"
BINARY_NAME="llm-gateway-go"

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
      echo "  --skip-build  跳过编译，使用现有二进制"
      echo "  --rollback    回滚到上一版本"
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
  phase "回滚 kaixuan-1"
  info "停止服务..."
  sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    pkill -f '$BINARY_NAME' || true
    sleep 2
  "
  
  info "恢复备份..."
  sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    cd $REMOTE_DIR
    BACKUP=\$(ls -t bin/${BINARY_NAME}.bak.* 2>/dev/null | head -1)
    if [ -z \"\$BACKUP\" ]; then
      echo '✗ 未找到备份文件'
      exit 1
    fi
    echo \"恢复备份: \$BACKUP\"
    cp \"\$BACKUP\" bin/$BINARY_NAME
    chmod +x bin/$BINARY_NAME
  "
  
  info "启动服务..."
  sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    cd $REMOTE_DIR
    nohup ./bin/$BINARY_NAME > logs/gateway.log 2>&1 &
    sleep 3
    pgrep -f '$BINARY_NAME' && echo '✓ 服务已启动' || echo '✗ 服务启动失败'
  "
  
  ok "回滚完成"
  exit 0
fi

# ── 前置检查 ──────────────────────────────────────────────────────
phase "前置检查"

if ! command -v sshpass &> /dev/null; then
  err "缺少 sshpass 命令"
  echo "安装: brew install sshpass"
  exit 1
fi

info "检查 SSH 连通性..."
if ! sshpass -e ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$SSH_USER@$SSH_HOST" "echo 'OK'" &>/dev/null; then
  err "无法连接到 $SSH_HOST"
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
info "当前 commit: $COMMIT_SHA"

# ── 拉取最新代码 ──────────────────────────────────────────────────
phase "拉取最新代码"

sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  cd $REMOTE_DIR
  echo '▶ git stash (如果有本地改动)'
  git stash 2>/dev/null || true
  echo '▶ git pull origin main'
  git pull origin main
  echo '▶ 最新 commit:'
  git log --oneline -1
"

ok "代码已拉取"

# ── 编译 ──────────────────────────────────────────────────────────
if ! $SKIP_BUILD; then
  phase "编译 (macOS ARM64)"
  
  sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
    cd $REMOTE_DIR
    
    # 设置 Go 环境
    export PATH=/usr/local/go/bin:/opt/homebrew/bin:\$PATH
    
    # 禁用代理，使用国内镜像
    unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy
    go env -w GOPROXY=https://goproxy.cn,direct
    
    echo '▶ go build -o bin/$BINARY_NAME ./cmd/gateway'
    go build -o bin/$BINARY_NAME ./cmd/gateway
    
    ls -lh bin/$BINARY_NAME
    file bin/$BINARY_NAME
  "
  
  ok "编译完成"
else
  info "跳过编译（--skip-build）"
fi

# ── 备份旧版本 ──────────────────────────────────────────────────
phase "备份旧版本"

sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  cd $REMOTE_DIR
  if [ -f bin/$BINARY_NAME ]; then
    BACKUP_NAME=bin/${BINARY_NAME}.bak.\$(date +%Y%m%d_%H%M%S)
    cp bin/$BINARY_NAME \"\$BACKUP_NAME\"
    echo \"✓ 备份完成: \$BACKUP_NAME\"
  else
    echo '⚠ 未找到旧版本'
  fi
"

# ── 停止旧服务 ──────────────────────────────────────────────────
phase "停止旧服务"

sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  pkill -f '$BINARY_NAME' || echo '⚠ 未找到运行中的进程'
  sleep 2
"

ok "旧服务已停止"

# ── 启动新服务 ──────────────────────────────────────────────────
phase "启动新服务"

sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  cd $REMOTE_DIR
  
  # 确保日志目录存在
  mkdir -p logs
  
  # 后台启动
  nohup ./bin/$BINARY_NAME > logs/gateway.log 2>&1 &
  sleep 3
  
  # 检查进程
  if pgrep -f '$BINARY_NAME' > /dev/null; then
    echo '✓ 服务已启动'
  else
    echo '✗ 服务启动失败'
    echo '查看日志: tail -50 logs/gateway.log'
    exit 1
  fi
"

ok "新服务已启动"

# ── 验证部署 ──────────────────────────────────────────────────
phase "验证部署"

info "检查进程..."
sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  ps aux | grep '$BINARY_NAME' | grep -v grep
"

info "查看最新日志..."
sshpass -e ssh -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "
  tail -30 $REMOTE_DIR/logs/gateway.log
"

# ── 完成 ──────────────────────────────────────────────────────
echo ""
ok "部署完成！"
echo ""
echo "下一步:"
echo "  1. 查看实时日志: ssh $SSH_USER@$SSH_HOST 'tail -f $REMOTE_DIR/logs/gateway.log'"
echo "  2. 测试通过后，部署到 154: ./deploy-154.sh"
echo "  3. 如果有问题，回滚: ./deploy-kaixuan1.sh --rollback"
