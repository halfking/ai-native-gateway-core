#!/bin/bash
#
# deploy-to-154-auto.sh - 自动化部署脚本（154生产环境）
#
# 功能：
#   1. 自动拉取最新代码
#   2. 交叉编译后端 (Linux AMD64)
#   3. 构建前端资源
#   4. 备份现有部署
#   5. 上传并部署到154服务器
#   6. 重启服务
#   7. 验证部署结果
#   8. 失败时自动回滚
#
# 使用方法：
#   ./scripts/deploy-to-154-auto.sh
#   ./scripts/deploy-to-154-auto.sh --skip-build  # 跳过编译（使用已有产物）
#   ./scripts/deploy-to-154-auto.sh --no-confirm  # 不需要确认直接部署
#
# 环境变量：
#   DEPLOY_SSH_HOST     - SSH主机 (默认: 47.97.111.154)
#   DEPLOY_SSH_PORT     - SSH端口 (默认: 25022)
#   DEPLOY_SSH_USER     - SSH用户 (默认: root)
#   DEPLOY_SSH_PASS     - SSH密码 (必需)
#   DEPLOY_TARGET_DIR   - 目标部署目录 (默认: /opt/llm-gateway-go)
#

set -e  # 遇到错误立即退出

# ============================================================================
# 配置
# ============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# SSH配置
SSH_HOST="${DEPLOY_SSH_HOST:-47.97.111.154}"
SSH_PORT="${DEPLOY_SSH_PORT:-25022}"
SSH_USER="${DEPLOY_SSH_USER:-root}"
SSH_PASS="${DEPLOY_SSH_PASS:-Kaixuan2026&#*9527}"
TARGET_DIR="${DEPLOY_TARGET_DIR:-/opt/llm-gateway-go}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 解析命令行参数
SKIP_BUILD=false
NO_CONFIRM=false
for arg in "$@"; do
  case $arg in
    --skip-build)
      SKIP_BUILD=true
      shift
      ;;
    --no-confirm)
      NO_CONFIRM=true
      shift
      ;;
    --help)
      echo "用法: $0 [选项]"
      echo ""
      echo "选项:"
      echo "  --skip-build   跳过编译步骤（使用已有构建产物）"
      echo "  --no-confirm   不需要确认直接部署"
      echo "  --help         显示此帮助信息"
      exit 0
      ;;
  esac
done

# ============================================================================
# 工具函数
# ============================================================================

log_info() {
  echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
  echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
  echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
  echo -e "${RED}[ERROR]${NC} $1"
}

run_ssh() {
  sshpass -p "$SSH_PASS" ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_USER@$SSH_HOST" "$@"
}

run_scp() {
  sshpass -p "$SSH_PASS" scp -P "$SSH_PORT" -o StrictHostKeyChecking=no "$@"
}

run_rsync() {
  sshpass -p "$SSH_PASS" rsync -avz -e "ssh -p $SSH_PORT -o StrictHostKeyChecking=no" "$@"
}

check_command() {
  if ! command -v "$1" &> /dev/null; then
    log_error "命令 '$1' 未找到，请先安装"
    exit 1
  fi
}

# ============================================================================
# 部署前检查
# ============================================================================

log_info "========================================"
log_info "  LLM Gateway 154 自动化部署"
log_info "========================================"
echo ""

# 检查必需命令
log_info "检查必需工具..."
check_command git
check_command go
check_command npm
check_command sshpass
check_command rsync
log_success "所有必需工具已安装"

# 检查工作目录
if [ ! -f "$PROJECT_DIR/go.mod" ]; then
  log_error "不在项目根目录下执行"
  exit 1
fi

# 检查SSH连接
log_info "测试SSH连接..."
if ! run_ssh "echo 'SSH连接成功'" &> /dev/null; then
  log_error "无法连接到 $SSH_HOST:$SSH_PORT"
  exit 1
fi
log_success "SSH连接正常"

# 获取当前分支和commit
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
CURRENT_COMMIT=$(git rev-parse --short HEAD)
COMMIT_MESSAGE=$(git log -1 --pretty=format:"%s")

log_info "当前分支: $CURRENT_BRANCH"
log_info "当前提交: $CURRENT_COMMIT - $COMMIT_MESSAGE"

# 检查是否有未提交的更改
if [ -n "$(git status --porcelain)" ]; then
  log_warn "工作目录有未提交的更改"
  if [ "$NO_CONFIRM" = false ]; then
    read -p "是否继续部署? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
      log_info "部署已取消"
      exit 0
    fi
  fi
fi

# ============================================================================
# 拉取最新代码
# ============================================================================

log_info "拉取最新代码..."
git pull origin "$CURRENT_BRANCH" || {
  log_warn "git pull 失败，继续使用当前代码"
}

LATEST_COMMIT=$(git rev-parse --short HEAD)
if [ "$LATEST_COMMIT" != "$CURRENT_COMMIT" ]; then
  log_success "代码已更新: $CURRENT_COMMIT -> $LATEST_COMMIT"
else
  log_info "代码已是最新"
fi

# ============================================================================
# 编译构建
# ============================================================================

if [ "$SKIP_BUILD" = true ]; then
  log_warn "跳过编译步骤"
else
  # 编译后端
  log_info "编译后端 (Linux AMD64)..."
  cd "$PROJECT_DIR"
  GOOS=linux GOARCH=amd64 go build -o llm-gateway-go ./cmd/gateway
  if [ ! -f "llm-gateway-go" ]; then
    log_error "后端编译失败"
    exit 1
  fi
  BINARY_SIZE=$(du -h llm-gateway-go | cut -f1)
  log_success "后端编译完成 (大小: $BINARY_SIZE)"

  # 构建前端
  log_info "构建前端..."
  cd "$PROJECT_DIR/web"
  
  # 检查依赖是否安装
  if [ ! -d "node_modules" ]; then
    log_info "安装前端依赖..."
    npm install
  fi
  
  npm run build
  if [ ! -d "dist" ]; then
    log_error "前端构建失败"
    exit 1
  fi
  FRONTEND_SIZE=$(du -sh dist | cut -f1)
  log_success "前端构建完成 (大小: $FRONTEND_SIZE)"
  
  cd "$PROJECT_DIR"
fi

# 确认部署
if [ "$NO_CONFIRM" = false ]; then
  echo ""
  log_warn "即将部署到生产环境："
  echo "  目标服务器: $SSH_HOST:$SSH_PORT"
  echo "  部署目录: $TARGET_DIR"
  echo "  Commit: $LATEST_COMMIT"
  echo ""
  read -p "确认部署? (y/N) " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    log_info "部署已取消"
    exit 0
  fi
fi

# ============================================================================
# 备份现有部署
# ============================================================================

BACKUP_NAME="backup-$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="$TARGET_DIR/backups/$BACKUP_NAME"

log_info "备份现有部署到 $BACKUP_NAME..."
run_ssh << EOF
  set -e
  cd $TARGET_DIR
  mkdir -p backups/$BACKUP_NAME
  
  # 备份二进制文件
  if [ -f llm-gateway-go ]; then
    cp llm-gateway-go backups/$BACKUP_NAME/
    echo "已备份二进制文件"
  fi
  
  # 备份前端文件
  if [ -d web ]; then
    cp -r web backups/$BACKUP_NAME/
    echo "已备份前端文件"
  fi
  
  # 清理旧备份（保留最近5个）
  cd backups
  ls -t | tail -n +6 | xargs -r rm -rf
  echo "已清理旧备份"
EOF

log_success "备份完成"

# ============================================================================
# 上传文件
# ============================================================================

log_info "上传后端文件..."
run_scp "$PROJECT_DIR/llm-gateway-go" "$SSH_USER@$SSH_HOST:$TARGET_DIR/"
log_success "后端上传完成"

log_info "上传前端文件..."
run_rsync "$PROJECT_DIR/web/dist/" "$SSH_USER@$SSH_HOST:$TARGET_DIR/web/"
log_success "前端上传完成"

# 设置可执行权限
log_info "设置文件权限..."
run_ssh "chmod +x $TARGET_DIR/llm-gateway-go"
log_success "权限设置完成"

# ============================================================================
# 重启服务
# ============================================================================

log_info "重启服务..."
run_ssh "systemctl stop llm-gateway-go.service && systemctl start llm-gateway-go.service"

# 等待服务启动
log_info "等待服务启动..."
sleep 3

# ============================================================================
# 验证部署
# ============================================================================

log_info "验证部署..."

# 检查服务状态
SERVICE_STATUS=$(run_ssh "systemctl is-active llm-gateway-go.service" || echo "failed")
if [ "$SERVICE_STATUS" != "active" ]; then
  log_error "服务启动失败"
  
  # 显示日志
  log_info "最近的服务日志:"
  run_ssh "journalctl -u llm-gateway-go.service -n 20 --no-pager"
  
  # 自动回滚
  log_warn "尝试回滚到上一个版本..."
  run_ssh << EOF
    cd $TARGET_DIR
    if [ -f backups/$BACKUP_NAME/llm-gateway-go ]; then
      cp backups/$BACKUP_NAME/llm-gateway-go .
      if [ -d backups/$BACKUP_NAME/web ]; then
        rm -rf web
        cp -r backups/$BACKUP_NAME/web .
      fi
      systemctl stop llm-gateway-go.service
      systemctl start llm-gateway-go.service
      echo "回滚完成"
    else
      echo "没有找到备份文件"
    fi
EOF
  
  log_error "部署失败，已尝试回滚"
  exit 1
fi

log_success "服务状态: $SERVICE_STATUS"

# 检查健康端点
log_info "检查健康端点..."
HEALTH_CHECK=$(curl -s -o /dev/null -w "%{http_code}" https://llm.kxpms.cn/healthz || echo "000")
if [ "$HEALTH_CHECK" = "200" ]; then
  log_success "健康检查通过 (HTTP $HEALTH_CHECK)"
else
  log_warn "健康检查失败 (HTTP $HEALTH_CHECK)"
fi

# 检查首页
log_info "检查首页..."
HOMEPAGE_CHECK=$(curl -s https://llm.kxpms.cn/ | grep -o "<title>LLM Gateway" || echo "")
if [ -n "$HOMEPAGE_CHECK" ]; then
  log_success "首页正常"
else
  log_warn "首页可能有问题，请手动检查"
fi

# 显示最近日志
log_info "最近的服务日志:"
run_ssh "journalctl -u llm-gateway-go.service -n 10 --no-pager | grep -E 'serving Vue SPA|CHECKPOINT|error|Error' || journalctl -u llm-gateway-go.service -n 5 --no-pager"

# ============================================================================
# 部署总结
# ============================================================================

echo ""
log_success "========================================"
log_success "  部署成功完成！"
log_success "========================================"
echo ""
echo "部署信息:"
echo "  - 时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo "  - Commit: $LATEST_COMMIT"
echo "  - 备份: $BACKUP_NAME"
echo "  - 访问地址: https://llm.kxpms.cn"
echo ""
echo "后续步骤:"
echo "  1. 访问 https://llm.kxpms.cn 验证首页"
echo "  2. 登录并测试仪表盘功能"
echo "  3. 检查浏览器控制台是否有错误"
echo "  4. 监控服务日志: journalctl -u llm-gateway-go.service -f"
echo ""

# 记录部署日志
DEPLOY_LOG="$PROJECT_DIR/DEPLOYMENT_LOG.txt"
echo "$(date '+%Y-%m-%d %H:%M:%S') | $LATEST_COMMIT | $COMMIT_MESSAGE | SUCCESS" >> "$DEPLOY_LOG"
log_info "部署记录已添加到 DEPLOYMENT_LOG.txt"
