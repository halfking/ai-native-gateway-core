#!/bin/bash
# ============================================================================
# File: deploy/phase0/deploy-245-simple.sh
# Purpose: Simple deployment to 245 using existing infrastructure
# Target: root@8.136.114.245:25022
# ============================================================================

set -euo pipefail

SERVER="8.136.114.245"
PORT="25022"
SSH="ssh -p $PORT root@$SERVER"
SCP="scp -P $PORT"

echo "========================================"
echo "  Phase 0 配置部署到 245"
echo "========================================"
echo "目标: ${SERVER}:${PORT}"
echo ""

# ============================================================================
# Step 1: Check connectivity
# ============================================================================
echo "=== Step 1: 检查连接 ==="
if $SSH "echo 'SSH OK'" > /dev/null 2>&1; then
    echo "✅ SSH 连接正常"
else
    echo "❌ 无法连接到 245"
    exit 1
fi

# ============================================================================
# Step 2: Backup current config
# ============================================================================
echo ""
echo "=== Step 2: 备份当前配置 ==="
BACKUP_DIR="/opt/llm-gateway/backups/pre-phase0-$(date +%Y%m%d-%H%M%S)"
$SSH "mkdir -p $BACKUP_DIR && \
    systemctl show llm-gateway | grep Environment > $BACKUP_DIR/env.txt 2>/dev/null || true"
echo "✅ 已备份到: $BACKUP_DIR"

# ============================================================================
# Step 3: Upload config
# ============================================================================
echo ""
echo "=== Step 3: 上传配置文件 ==="
$SSH "mkdir -p /opt/llm-gateway/config"
$SCP deploy/phase0/optimization.env root@${SERVER}:/opt/llm-gateway/config/
echo "✅ 配置已上传"

# ============================================================================
# Step 4: Apply config via systemd
# ============================================================================
echo ""
echo "=== Step 4: 应用配置 ==="
$SSH "mkdir -p /etc/systemd/system/llm-gateway.service.d && \
    cat > /etc/systemd/system/llm-gateway.service.d/phase0.conf <<'EOC'
[Service]
EnvironmentFile=/opt/llm-gateway/config/optimization.env
EOC"
echo "✅ Systemd 配置已更新"

# ============================================================================
# Step 5: Restart service
# ============================================================================
echo ""
echo "=== Step 5: 重启服务 ==="
$SSH "systemctl daemon-reload && systemctl restart llm-gateway"
echo "等待服务启动..."
sleep 15

# ============================================================================
# Step 6: Health check
# ============================================================================
echo ""
echo "=== Step 6: 健康检查 ==="
for i in {1..10}; do
    echo "尝试 $i/10..."
    if $SSH "curl -fsS http://localhost:8781/healthz" > /dev/null 2>&1; then
        echo "✅ 健康检查通过"
        break
    fi
    sleep 3
done

# ============================================================================
# Step 7: Verify
# ============================================================================
echo ""
echo "=== Step 7: 验证部署 ==="
echo "服务状态:"
$SSH "systemctl status llm-gateway --no-pager | head -10"

echo ""
echo "========================================"
echo "  部署完成"
echo "========================================"
echo ""
echo "下一步: 运行完整验证"
echo "  bash deploy/phase0/verify.sh 245"
echo ""
echo "回滚命令:"
echo "  $SSH 'rm /etc/systemd/system/llm-gateway.service.d/phase0.conf && systemctl daemon-reload && systemctl restart llm-gateway'"
