#!/bin/bash
# deploy-to-154.sh - 部署到154主机（直接部署模式）
#
# 用法:
#   ./deploy-to-154.sh [--skip-build] [--skip-frontend]
#
# 选项:
#   --skip-build     跳过后端编译
#   --skip-frontend  跳过前端构建

set -euo pipefail

# 配置
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
REMOTE_USER="${DEPLOY_USER:-root}"
REMOTE_HOST="${DEPLOY_HOST:-llm.kxpms.cn}"
REMOTE_PORT="${DEPLOY_PORT:-22}"
REMOTE_DIR="${REMOTE_DIR:-/opt/llm-gateway-go}"
SERVICE_NAME="${SERVICE_NAME:-llm-gateway}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 参数解析
SKIP_BUILD=0
SKIP_FRONTEND=0

while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-build)
      SKIP_BUILD=1
      shift
      ;;
    --skip-frontend)
      SKIP_FRONTEND=1
      shift
      ;;
    *)
      echo -e "${RED}Unknown option: $1${NC}"
      exit 1
      ;;
  esac
done

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}  LLM Gateway 部署脚本 - 154主机${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# 步骤 1: 编译后端
if [ $SKIP_BUILD -eq 0 ]; then
  echo -e "${YELLOW}[1/5]${NC} 编译后端..."
  cd "$REPO_DIR"
  go build -o bin/llm-gateway-go ./cmd/gateway
  if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 后端编译成功${NC}"
  else
    echo -e "${RED}✗ 后端编译失败${NC}"
    exit 1
  fi
else
  echo -e "${YELLOW}[1/5]${NC} 跳过后端编译"
fi

# 步骤 2: 构建前端
if [ $SKIP_FRONTEND -eq 0 ]; then
  echo -e "${YELLOW}[2/5]${NC} 构建前端..."
  cd "$REPO_DIR/web"
  npm run build
  if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 前端构建成功${NC}"
  else
    echo -e "${RED}✗ 前端构建失败${NC}"
    exit 1
  fi
else
  echo -e "${YELLOW}[2/5]${NC} 跳过前端构建"
fi

# 步骤 3: 备份远程服务
echo -e "${YELLOW}[3/5]${NC} 备份远程二进制文件..."
ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" "
  if [ -f $REMOTE_DIR/gateway ]; then
    cp $REMOTE_DIR/gateway $REMOTE_DIR/gateway.bak.\$(date +%Y%m%d_%H%M%S)
    echo '✓ 备份完成'
  else
    echo '⚠ 未找到旧版本，跳过备份'
  fi
"

# 步骤 4: 上传新版本
echo -e "${YELLOW}[4/5]${NC} 上传新版本到服务器..."

# 上传后端
echo "  - 上传后端二进制..."
scp -P "$REMOTE_PORT" "$REPO_DIR/bin/llm-gateway-go" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/gateway.new"

# 上传前端
echo "  - 上传前端资源..."
ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" "mkdir -p $REMOTE_DIR/web"
rsync -avz -e "ssh -p $REMOTE_PORT" --delete "$REPO_DIR/web/dist/" "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/web/"

echo -e "${GREEN}✓ 上传完成${NC}"

# 步骤 5: 重启服务
echo -e "${YELLOW}[5/5]${NC} 重启服务..."
ssh -p "$REMOTE_PORT" "$REMOTE_USER@$REMOTE_HOST" "
  set -e
  
  # 替换二进制
  mv $REMOTE_DIR/gateway.new $REMOTE_DIR/gateway
  chmod +x $REMOTE_DIR/gateway
  
  # 重启服务
  if command -v systemctl &> /dev/null; then
    echo '使用 systemctl 重启服务...'
    sudo systemctl restart $SERVICE_NAME || true
  elif command -v supervisorctl &> /dev/null; then
    echo '使用 supervisorctl 重启服务...'
    sudo supervisorctl restart $SERVICE_NAME || true
  else
    echo '⚠ 未检测到服务管理器，需要手动重启服务'
    echo '请执行: pkill -f gateway && cd $REMOTE_DIR && nohup ./gateway &'
  fi
  
  echo '✓ 服务重启完成'
"

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  部署完成！${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo -e "验证步骤："
echo -e "  1. 访问: ${BLUE}https://llm.kxpms.cn${NC}"
echo -e "  2. 检查仪表盘页面是否正常"
echo -e "  3. 检查 Console 是否还有错误"
echo ""
echo -e "如需回滚，请执行："
echo -e "  ssh -p $REMOTE_PORT $REMOTE_USER@$REMOTE_HOST"
echo -e "  cd $REMOTE_DIR && ls -lt gateway.bak.* | head -1"
echo -e "  # 复制最新备份文件名，然后执行："
echo -e "  cp gateway.bak.YYYYMMDD_HHMMSS gateway && systemctl restart $SERVICE_NAME"
echo ""
