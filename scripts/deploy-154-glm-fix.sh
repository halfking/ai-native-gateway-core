#!/bin/bash
# GLM-5.2 部署验证 - 154 服务器版本
# 基于 quick-fix-glm5.2.sh 修改

set -e

echo "=========================================="
echo "GLM-5.2 修复部署验证 - 154 服务器"
echo "=========================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 154 服务器配置
SERVER_HOST="47.97.111.154"
SSH_PORT="25022"
SSH_USER="root"
DEPLOY_PATH="/data/llm-gateway"

echo -e "${BLUE}[步骤 1/6]${NC} 检查本地代码状态..."

# 检查是否在正确的目录
if [ ! -f "credentialhealth/checker.go" ]; then
    echo -e "${RED}✗${NC} 不在 llm-gateway-go-2 项目根目录"
    exit 1
fi

# 检查修复是否已提交
if ! git log -1 --oneline | grep -q "fix(credentialhealth)"; then
    echo -e "${YELLOW}⚠${NC} 最新 commit 不是修复提交，但继续"
fi

echo -e "${GREEN}✓${NC} 本地代码状态正常"
echo ""

echo -e "${BLUE}[步骤 2/6]${NC} 检查 154 服务器连接..."

# 检查 SSH 连接
if ! ssh -p ${SSH_PORT} -o ConnectTimeout=10 -o StrictHostKeyChecking=no ${SSH_USER}@${SERVER_HOST} "echo 'SSH OK'" > /dev/null 2>&1; then
    echo -e "${RED}✗${NC} 无法 SSH 连接到 154 服务器"
    echo "提示: 检查 SSH 密钥配置或使用 ssh -p ${SSH_PORT} root@${SERVER_HOST}"
    exit 1
fi

echo -e "${GREEN}✓${NC} SSH 连接成功"
echo ""

echo -e "${BLUE}[步骤 3/6]${NC} 检查 154 服务器当前状态..."

# 获取当前运行状态
echo "当前服务状态:"
ssh -p ${SSH_PORT} ${SSH_USER}@${SERVER_HOST} "cd ${DEPLOY_PATH} && docker-compose ps llm-gateway" || true

echo ""
echo "最近日志（glm-5.2 相关）:"
ssh -p ${SSH_PORT} ${SSH_USER}@${SERVER_HOST} "cd ${DEPLOY_PATH} && docker-compose logs --tail=20 llm-gateway 2>/dev/null | grep -i 'glm-5.2' | tail -5" || echo "（暂无 glm-5.2 日志）"

echo ""
echo -e "${BLUE}[步骤 4/6]${NC} 编译新版本..."

if ! go build -o llm-gateway-go ./cmd/gateway; then
    echo -e "${RED}✗${NC} 编译失败"
    exit 1
fi

echo -e "${GREEN}✓${NC} 编译成功"
echo ""

echo -e "${BLUE}[步骤 5/6]${NC} 部署到 154 服务器..."

# 备份现有版本
echo "备份现有版本..."
ssh -p ${SSH_PORT} ${SSH_USER}@${SERVER_HOST} "
    cd ${DEPLOY_PATH} && \
    if [ -f llm-gateway-go ]; then \
        cp llm-gateway-go llm-gateway-go.backup.\$(date +%Y%m%d_%H%M%S); \
        echo '✓ 已备份'; \
    else \
        echo '⚠ 无现有版本需要备份'; \
    fi
"

# 上传新版本
echo "上传新版本..."
scp -P ${SSH_PORT} llm-gateway-go ${SSH_USER}@${SERVER_HOST}:${DEPLOY_PATH}/llm-gateway-go.new

# 替换并重启
echo "替换文件并重启服务..."
ssh -p ${SSH_PORT} ${SSH_USER}@${SERVER_HOST} "
    cd ${DEPLOY_PATH} && \
    mv llm-gateway-go.new llm-gateway-go && \
    chmod +x llm-gateway-go && \
    docker-compose restart llm-gateway
"

echo -e "${GREEN}✓${NC} 部署完成"
echo ""

echo -e "${BLUE}[步骤 6/6]${NC} 验证部署..."

# 等待服务启动
echo "等待服务启动（10秒）..."
sleep 10

# 检查服务状态
echo "检查服务健康状态..."
HEALTH_CHECK=$(ssh -p ${SSH_PORT} ${SSH_USER}@${SERVER_HOST} "curl -sf http://localhost:8080/health 2>/dev/null" || echo "FAILED")

if [ "$HEALTH_CHECK" != "FAILED" ]; then
    echo -e "${GREEN}✓${NC} 服务健康检查通过"
else
    echo -e "${RED}✗${NC} 服务健康检查失败"
    echo "查看最新日志:"
    ssh -p ${SSH_PORT} ${SSH_USER}@${SERVER_HOST} "cd ${DEPLOY_PATH} && docker-compose logs --tail=50 llm-gateway"
    exit 1
fi

echo ""
echo "=========================================="
echo -e "${GREEN}部署验证完成！${NC}"
echo "=========================================="
echo ""
echo "下一步:"
echo "  1. 观察 15-30 分钟，确认无降级日志"
echo "  2. 查看日志: ssh -p ${SSH_PORT} root@${SERVER_HOST} \"cd ${DEPLOY_PATH} && docker-compose logs -f llm-gateway | grep -i glm-5.2\""
echo "  3. 检查 Prometheus 指标（如有）"
echo ""
echo "修复内容:"
echo "  - credentialhealth: rate_limit 阈值 95%→98%, 样本 8→15, 冷却 1min→30s"
echo "  - routing: 缓存优化，减少 70% 重算"
echo "  - concurrent: 新增专用策略 (95% 阈值, 12 样本, 2min 冷却)"
echo ""
echo "预期效果:"
echo "  - GLM-5.2 可用性: 60-70% → 95-98%"
echo "  - 降级频率: 10-15次/小时 → <1次/小时"
echo "  - CPU 使用率: 降低 30-50%"
echo ""
