#!/bin/bash
# W1 缺口① - 245 测试环境功能验证脚本
# 验证 per-model 熔断器在真实环境中的行为

set -euo pipefail

TARGET="http://245.itestu.cn:12112"
API_KEY="${API_KEY:-test-key-please-set}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[✓]${NC} $*"; }
log_warning() { echo -e "${YELLOW}[!]${NC} $*"; }
log_error() { echo -e "${RED}[✗]${NC} $*"; }

echo "=========================================="
echo "W1 缺口① - 245 环境功能验证"
echo "=========================================="
echo ""

# 1. 健康检查
log_info "1. 健康检查..."
if curl -sf "$TARGET/health" > /dev/null; then
    log_success "健康检查通过"
else
    log_error "健康检查失败"
    exit 1
fi

# 2. 版本检查
log_info "2. 版本检查..."
VERSION=$(curl -sf "$TARGET/version" | jq -r '.git_sha')
log_info "当前版本: $VERSION"
if [[ "$VERSION" == "feeb01eb" ]] || [[ "$VERSION" =~ ^15c54de ]]; then
    log_success "版本包含 per-model 改造"
else
    log_warning "版本可能不包含 per-model 改造，请确认"
fi

# 3. 检查日志中的 per-model 键
log_info "3. 检查日志格式..."
ssh 245 "journalctl -u 'llmgo-245-canary@8782' -n 100 --no-pager | grep -i circuit || true" | tail -5

echo ""
log_info "如果看到 'key=providerID/model' 格式（如 key=1/gpt-4），说明 per-model 已生效"
echo ""

# 4. 查询 DB 状态
log_info "4. 查询 credentials 表..."
ssh 245 "cd /opt/llm-gateway-go && source .env && psql -h \$DB_HOST -U \$DB_USER -d \$DB_NAME -c 'SELECT id, provider_id, circuit_state FROM credentials WHERE circuit_state != '\"'\"'closed'\"'\"' LIMIT 5;' 2>&1 | head -20"

echo ""
log_success "验收完成！"
echo ""
echo "=========================================="
echo "后续手动验证步骤："
echo "1. 模拟单模型故障（需要真实 API key）"
echo "2. 观察日志中的 per-model 键"
echo "3. 验证其他模型不受影响"
echo "=========================================="
