#!/bin/bash
# 部署修复到71服务器
# 使用方法: ./scripts/deploy_fix_to_71.sh [服务器地址]

set -e

SERVER="${1:-root@llmgateway.internal.example.com}"
REMOTE_PATH="/opt/llm-gateway-go"

echo "=========================================="
echo "部署修复到71服务器"
echo "=========================================="
echo "目标服务器: $SERVER"
echo "远程路径: $REMOTE_PATH"
echo ""

# 1. 检查当前分支
CURRENT_BRANCH=$(git branch --show-current)
echo "当前分支: $CURRENT_BRANCH"

if [ "$CURRENT_BRANCH" != "server-71" ]; then
    echo "⚠️  警告: 当前不在 server-71 分支"
    read -p "是否继续? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# 2. 显示最新提交
echo ""
echo "最近的提交:"
git log --oneline -5
echo ""

# 3. 同步代码到服务器
echo "同步代码到服务器..."
rsync -avz --exclude='.git' --exclude='*.log' --exclude='tmp/' \
    ./ "$SERVER:$REMOTE_PATH/"

# 4. 在服务器上执行编译和重启
echo ""
echo "在服务器上编译和重启..."
ssh "$SERVER" << 'REMOTE_SCRIPT'
cd /opt/llm-gateway-go

# 备份当前可执行文件
if [ -f "llm-gateway" ]; then
    cp llm-gateway llm-gateway.backup.$(date +%Y%m%d_%H%M%S)
fi

# 编译
echo "编译新版本..."
go build -o llm-gateway ./cmd/gateway

# 检查编译是否成功
if [ ! -f "llm-gateway" ]; then
    echo "❌ 编译失败"
    exit 1
fi

echo "✓ 编译成功"

# 重启服务
echo "重启服务..."
systemctl restart llm-gateway

# 等待服务启动
sleep 3

# 检查服务状态
if systemctl is-active --quiet llm-gateway; then
    echo "✓ 服务启动成功"
else
    echo "❌ 服务启动失败"
    journalctl -u llm-gateway -n 50 --no-pager
    exit 1
fi
REMOTE_SCRIPT

# 5. 运行健康检查
echo ""
echo "运行健康检查..."
sleep 2

curl -s http://llmgateway.internal.example.com/healthz || echo "⚠️  健康检查失败"

echo ""
echo "=========================================="
echo "部署完成"
echo "=========================================="
echo ""
echo "后续操作:"
echo "1. ssh $SERVER"
echo "2. journalctl -u llm-gateway -f  # 查看日志"
echo "3. 运行诊断脚本测试"
echo ""
