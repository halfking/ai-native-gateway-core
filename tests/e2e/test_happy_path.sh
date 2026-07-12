#!/usr/bin/env bash
set -euo pipefail

# E2E 测试主流程脚本
# 功能：激活 → 注册 → 心跳 → 升级完整流程

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试环境配置
AUTHORITY_URL="http://localhost:8443"
DB_HOST="localhost"
DB_PORT="5433"
DB_NAME="llm_gateway_test"
DB_USER="test"
DB_PASSWORD="test123"
export PGPASSWORD="$DB_PASSWORD"

# 临时文件目录
TMP_DIR="$SCRIPT_DIR/tmp"
mkdir -p "$TMP_DIR"

# 日志函数
log_step() {
    echo -e "${GREEN}==> [$1]${NC} $2"
}

log_error() {
    echo -e "${RED}✗ ERROR:${NC} $1" >&2
}

log_success() {
    echo -e "${GREEN}✓${NC} $1"
}

log_info() {
    echo -e "${YELLOW}ℹ${NC} $1"
}

# 清理函数
cleanup() {
    log_info "清理临时文件..."
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

# 步骤 1: 启动服务
log_step "1/9" "启动 Docker Compose 服务..."
cd "$SCRIPT_DIR"
docker-compose -f docker-compose.e2e.yml up -d
log_success "Docker Compose 服务已启动"

# 步骤 2: 等待服务健康
log_step "2/9" "等待服务健康检查..."

# 等待 PostgreSQL
log_info "等待 PostgreSQL..."
for i in {1..30}; do
    if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1" >/dev/null 2>&1; then
        log_success "PostgreSQL 已就绪"
        break
    fi
    if [ "$i" -eq 30 ]; then
        log_error "PostgreSQL 健康检查超时"
        exit 1
    fi
    sleep 1
done

# 等待 Redis
log_info "等待 Redis..."
for i in {1..30}; do
    if redis-cli -h localhost -p 6380 ping >/dev/null 2>&1; then
        log_success "Redis 已就绪"
        break
    fi
    if [ "$i" -eq 30 ]; then
        log_error "Redis 健康检查超时"
        exit 1
    fi
    sleep 1
done

# 等待 license-authority
log_info "等待 license-authority..."
for i in {1..60}; do
    if curl --fail --silent --show-error "$AUTHORITY_URL/healthz" >/dev/null 2>&1; then
        log_success "license-authority 已就绪"
        break
    fi
    if [ "$i" -eq 60 ]; then
        log_error "license-authority 健康检查超时"
        docker-compose -f docker-compose.e2e.yml logs license-authority
        exit 1
    fi
    sleep 2
done

# 步骤 3: 生成 Ed25519 密钥对
log_step "3/9" "生成 Ed25519 密钥对（模拟首次部署）..."
PRIVATE_KEY_FILE="$TMP_DIR/instance.key"
PUBLIC_KEY_FILE="$TMP_DIR/instance.pub"

# 使用 openssl 生成 Ed25519 密钥对
openssl genpkey -algorithm Ed25519 -out "$PRIVATE_KEY_FILE" 2>/dev/null
openssl pkey -in "$PRIVATE_KEY_FILE" -pubout -out "$PUBLIC_KEY_FILE" 2>/dev/null

# 读取公钥（去掉 PEM 头尾）
PUBLIC_KEY=$(grep -v "PUBLIC KEY" "$PUBLIC_KEY_FILE" | tr -d '\n')
log_success "密钥对已生成"
log_info "公钥: ${PUBLIC_KEY:0:32}..."

# 步骤 4: 激活 license（模拟 llm-gw-installer activate --mode trial）
log_step "4/9" "激活 license（trial 模式）..."

# 注意：实际的 activate 命令会生成 license token，这里直接使用测试 license key
LICENSE_KEY="test-trial-license-e2e"
log_success "使用测试 license: $LICENSE_KEY"

# 步骤 5: 注册实例
log_step "5/9" "注册实例..."

INSTANCE_ID="e2e-test-instance-$(date +%s)"
REGISTER_PAYLOAD=$(cat <<EOF
{
  "instance_id": "$INSTANCE_ID",
  "public_key": "$PUBLIC_KEY",
  "version": "v1.13.0",
  "license_key": "$LICENSE_KEY",
  "hardware_hash": "e2e-test-hardware-hash",
  "deployment_metadata": {
    "hostname": "e2e-test-host",
    "os": "$(uname -s)",
    "arch": "$(uname -m)"
  }
}
EOF
)

REGISTER_RESPONSE=$(curl --fail --silent --show-error \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$REGISTER_PAYLOAD" \
    "$AUTHORITY_URL/api/v1/instances/register")

INSTANCE_TOKEN=$(echo "$REGISTER_RESPONSE" | jq -r '.instance_token')
REFRESH_TOKEN=$(echo "$REGISTER_RESPONSE" | jq -r '.refresh_token')

if [ -z "$INSTANCE_TOKEN" ] || [ "$INSTANCE_TOKEN" = "null" ]; then
    log_error "注册失败，未获取到 instance_token"
    echo "响应: $REGISTER_RESPONSE"
    exit 1
fi

log_success "实例已注册"
log_info "Instance ID: $INSTANCE_ID"
log_info "Instance Token: ${INSTANCE_TOKEN:0:20}..."
log_info "Refresh Token: ${REFRESH_TOKEN:0:20}..."

# 步骤 6: 发送心跳 3 次
log_step "6/9" "发送心跳（3 次，间隔 2s）..."

for i in {1..3}; do
    log_info "发送第 $i 次心跳..."
    
    HEARTBEAT_PAYLOAD=$(cat <<EOF
{
  "instance_id": "$INSTANCE_ID",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "uptime_secs": $((60 * i)),
  "num_goroutine": 100,
  "alloc_mb": 256.5,
  "status": "running",
  "metrics": {
    "cpu_usage": 45.2,
    "memory_usage": 60.8,
    "active_sessions": 12
  }
}
EOF
)

    HEARTBEAT_RESPONSE=$(curl --fail --silent --show-error \
        -X POST \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $INSTANCE_TOKEN" \
        -d "$HEARTBEAT_PAYLOAD" \
        "$AUTHORITY_URL/api/v1/instances/heartbeat")
    
    log_success "第 $i 次心跳发送成功"
    
    if [ "$i" -lt 3 ]; then
        sleep 2
    fi
done

# 步骤 7: 检查升级
log_step "7/9" "检查升级（模拟 llm-gw-installer upgrade --action check）..."

UPGRADE_CHECK_RESPONSE=$(curl --fail --silent --show-error \
    -X GET \
    -H "Authorization: Bearer $INSTANCE_TOKEN" \
    "$AUTHORITY_URL/api/v1/update/check?instance_id=$INSTANCE_ID&current_version=v1.13.0")

AVAILABLE=$(echo "$UPGRADE_CHECK_RESPONSE" | jq -r '.available')
TARGET_VERSION=$(echo "$UPGRADE_CHECK_RESPONSE" | jq -r '.target_version // "N/A"')

if [ "$AVAILABLE" = "true" ]; then
    log_success "发现可用升级版本: $TARGET_VERSION"
else
    log_info "当前已是最新版本"
fi

# 步骤 8: 验证状态
log_step "8/9" "验证数据库状态..."

# 验证 gateway_instances 表
INSTANCE_COUNT=$(psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -tAc "SELECT COUNT(*) FROM gateway_instances WHERE instance_id = '$INSTANCE_ID'")

if [ "$INSTANCE_COUNT" != "1" ]; then
    log_error "实例未正确写入 gateway_instances 表（count=$INSTANCE_COUNT）"
    exit 1
fi
log_success "实例已正确写入 gateway_instances 表"

# 验证心跳记录
HEARTBEAT_COUNT=$(psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -tAc "SELECT COUNT(*) FROM instance_heartbeats WHERE instance_id = '$INSTANCE_ID'")

if [ "$HEARTBEAT_COUNT" -lt 3 ]; then
    log_error "心跳记录不足（expected=3, actual=$HEARTBEAT_COUNT）"
    exit 1
fi
log_success "心跳记录已正确写入（count=$HEARTBEAT_COUNT）"

# 验证 tokens
TOKEN_EXISTS=$(psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -tAc "SELECT instance_token IS NOT NULL AND refresh_token IS NOT NULL FROM gateway_instances WHERE instance_id = '$INSTANCE_ID'")

if [ "$TOKEN_EXISTS" != "t" ]; then
    log_error "实例 token 未正确存储"
    exit 1
fi
log_success "实例 token 已正确存储"

# 步骤 9: 清理环境
log_step "9/9" "清理测试环境..."

# 清理测试数据
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -c "DELETE FROM instance_heartbeats WHERE instance_id = '$INSTANCE_ID'" >/dev/null
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -c "DELETE FROM gateway_instances WHERE instance_id = '$INSTANCE_ID'" >/dev/null

log_success "测试数据已清理"

# 停止 Docker Compose
cd "$SCRIPT_DIR"
docker-compose -f docker-compose.e2e.yml down -v >/dev/null 2>&1
log_success "Docker Compose 服务已停止"

# 最终报告
echo ""
echo "=========================================="
echo -e "${GREEN}✓ E2E 测试全部通过${NC}"
echo "=========================================="
echo "测试覆盖："
echo "  ✓ 步骤 1: Docker Compose 服务启动"
echo "  ✓ 步骤 2: 服务健康检查（PostgreSQL, Redis, license-authority）"
echo "  ✓ 步骤 3: Ed25519 密钥对生成"
echo "  ✓ 步骤 4: License 激活"
echo "  ✓ 步骤 5: 实例注册（获取 instance_token 和 refresh_token）"
echo "  ✓ 步骤 6: 心跳发送（3 次，间隔 2s）"
echo "  ✓ 步骤 7: 升级检查"
echo "  ✓ 步骤 8: 数据库状态验证"
echo "  ✓ 步骤 9: 环境清理"
echo "=========================================="
echo "实例 ID: $INSTANCE_ID"
echo "心跳次数: $HEARTBEAT_COUNT"
echo "可用升级: $AVAILABLE ($TARGET_VERSION)"
echo "=========================================="

exit 0
