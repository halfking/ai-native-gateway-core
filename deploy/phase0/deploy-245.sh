#!/bin/bash
# ============================================================================
# File: deploy/phase0/deploy-245.sh
# Purpose: Deploy Phase 0 optimization to 245 server
# Target: root@8.136.114.245:25022
# Reference: skills/deploy-245/SKILL.md
# ============================================================================

set -euo pipefail

SERVER_245="8.136.114.245"
SSH_PORT="25022"
SSH_TARGET="root@${SERVER_245} -p ${SSH_PORT}"

echo "========================================"
echo "  Phase 0 部署到 245 服务器"
echo "========================================"
echo "目标: ${SERVER_245}:${SSH_PORT}"
echo ""

# ============================================================================
# Step 1: Pre-deployment checks
# ============================================================================
echo "=== Step 1: 部署前检查 ==="

# Check SSH connectivity
if ! ssh $SSH_TARGET "echo 'SSH OK'" > /dev/null 2>&1; then
    echo "❌ 无法连接到 245 服务器"
    exit 1
fi
echo "✅ SSH 连接正常"

# Check current service status
CURRENT_STATUS=$(ssh $SSH_TARGET "systemctl is-active llm-gateway 2>/dev/null || echo 'inactive'")
echo "当前服务状态: $CURRENT_STATUS"

# ============================================================================
# Step 2: Backup current config
# ============================================================================
echo ""
echo "=== Step 2: 备份当前配置 ==="

BACKUP_DIR="/opt/llm-gateway/backups/pre-phase0-$(date +%Y%m%d-%H%M%S)"
ssh $SSH_TARGET "mkdir -p $BACKUP_DIR && \
    cp /opt/llm-gateway/bin/gateway $BACKUP_DIR/ 2>/dev/null || true && \
    cp /etc/systemd/system/llm-gateway.service $BACKUP_DIR/ 2>/dev/null || true && \
    cp /etc/systemd/system/llm-gateway.service.d/override.conf $BACKUP_DIR/ 2>/dev/null || true"
echo "✅ 已备份到: $BACKUP_DIR"

# ============================================================================
# Step 3: Upload new binary and config
# ============================================================================
echo ""
echo "=== Step 3: 上传二进制和配置 ==="

# Build latest binary
echo "编译最新二进制..."
go build -o bin/gateway cmd/gateway/main.go
echo "✅ 编译完成"

# Upload binary
echo "上传二进制文件..."
scp -P $SSH_PORT bin/gateway $SSH_TARGET:/opt/llm-gateway/bin/
echo "✅ 二进制已上传"

# Upload config
echo "上传配置文件..."
ssh $SSH_TARGET "mkdir -p /opt/llm-gateway/config"
scp -P $SSH_PORT deploy/phase0/optimization.env $SSH_TARGET:/opt/llm-gateway/config/
echo "✅ 配置已上传"

# ============================================================================
# Step 4: Create systemd override
# ============================================================================
echo ""
echo "=== Step 4: 配置 systemd ==="

ssh $SSH_TARGET "mkdir -p /etc/systemd/system/llm-gateway.service.d && \
    cat > /etc/systemd/system/llm-gateway.service.d/phase0-optimization.conf <<EOC
[Service]
EnvironmentFile=/opt/llm-gateway/config/optimization.env
EOC"
echo "✅ Systemd override 已创建"

# ============================================================================
# Step 5: Restart service
# ============================================================================
echo ""
echo "=== Step 5: 重启服务 ==="

ssh $SSH_TARGET "systemctl daemon-reload && systemctl restart llm-gateway"
echo "等待服务启动..."
sleep 10

# ============================================================================
# Step 6: Health check
# ============================================================================
echo ""
echo "=== Step 6: 健康检查 ==="

HEALTH_OK=false
for i in {1..10}; do
    echo "尝试 $i/10..."
    if ssh $SSH_TARGET "curl -fsS http://localhost:8781/healthz" > /dev/null 2>&1; then
        HEALTH_OK=true
        break
    fi
    sleep 3
done

if [ "$HEALTH_OK" = true ]; then
    echo "✅ 健康检查通过"
else
    echo "❌ 健康检查失败"
    echo ""
    echo "回滚命令:"
    echo "  ssh $SSH_TARGET 'cp $BACKUP_DIR/gateway /opt/llm-gateway/bin/ && systemctl restart llm-gateway'"
    exit 1
fi

# ============================================================================
# Step 7: Verify configuration
# ============================================================================
echo ""
echo "=== Step 7: 验证配置 ==="

echo "检查环境变量..."
ssh $SSH_TARGET "systemctl show llm-gateway | grep -i 'MAX_IDLE_CONNS'" || echo "⚠️  无法读取配置"

echo ""
echo "检查服务状态..."
ssh $SSH_TARGET "systemctl status llm-gateway --no-pager | head -15"

# ============================================================================
# Step 8: Performance baseline
# ============================================================================
echo ""
echo "=== Step 8: 性能基准测试 ==="

echo "运行快速性能测试（20次请求）..."
TOTAL_TIME=0
SUCCESS=0

for i in {1..20}; do
    RESPONSE_TIME=$(ssh $SSH_TARGET "curl -w '%{time_total}' -o /dev/null -s http://localhost:8781/healthz" 2>/dev/null || echo "0")
    if [[ "$RESPONSE_TIME" != "0" ]]; then
        TOTAL_TIME=$(echo "$TOTAL_TIME + $RESPONSE_TIME" | bc)
        SUCCESS=$((SUCCESS + 1))
    fi
    sleep 0.5
done

if [ $SUCCESS -gt 0 ]; then
    AVG_TIME=$(echo "scale=4; $TOTAL_TIME / $SUCCESS" | bc)
    echo "✅ 平均响应时间: ${AVG_TIME}s (${SUCCESS}/20 成功)"
else
    echo "❌ 性能测试失败"
fi

echo ""
echo "========================================"
echo "  部署完成"
echo "========================================"
echo "服务器: ${SERVER_245}:${SSH_PORT}"
echo "备份: $BACKUP_DIR"
echo ""
echo "监控命令:"
echo "  ssh $SSH_TARGET 'journalctl -u llm-gateway -f'"
echo ""
echo "回滚命令:"
echo "  ssh $SSH_TARGET 'bash /opt/llm-gateway/rollback-optimization.sh'"
echo ""
echo "下一步: 运行完整验证测试"
echo "  bash deploy/phase0/verify.sh 245"
