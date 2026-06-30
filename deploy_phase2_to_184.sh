#!/bin/bash
set -e

# Phase 2 热度追踪功能部署脚本（184测试环境）
# 用法: ./deploy_phase2_to_184.sh

SERVER="root@8.155.23.184"
REMOTE_DIR="/data/services/llm-gateway-go"
BACKUP_DIR="/data/backups/llm-gateway-go"

echo "=== Phase 2 热度追踪功能部署 ==="
echo ""

# 1. 本地构建
echo "1. 本地构建二进制..."
GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux-amd64 ./cmd/gateway/
if [ ! -f llm-gateway-go-linux-amd64 ]; then
    echo "✗ 构建失败"
    exit 1
fi
echo "✓ 构建成功: $(ls -lh llm-gateway-go-linux-amd64 | awk '{print $5}')"
echo ""

# 2. 上传到服务器
echo "2. 上传到 184 服务器..."
scp llm-gateway-go-linux-amd64 $SERVER:/tmp/llm-gateway-go-new
echo "✓ 上传完成"
echo ""

# 3. 远程部署
echo "3. 远程部署并重启..."
ssh $SERVER << 'REMOTE_SCRIPT'
set -e
cd /data/services/llm-gateway-go

# 备份当前版本
echo "  → 备份当前版本..."
mkdir -p /data/backups/llm-gateway-go
cp llm-gateway-go /data/backups/llm-gateway-go/llm-gateway-go.$(date +%Y%m%d_%H%M%S) || true

# 停止服务
echo "  → 停止服务..."
systemctl stop llm-gateway || supervisorctl stop llm-gateway || killall llm-gateway-go || true
sleep 2

# 替换二进制
echo "  → 替换二进制..."
mv /tmp/llm-gateway-go-new llm-gateway-go
chmod +x llm-gateway-go

# 检查 .env 配置
echo "  → 检查配置..."
if ! grep -q "LLM_GATEWAY_ENABLE_POPULARITY_TRACKING" .env 2>/dev/null; then
    echo "  → 添加热度追踪配置（默认禁用）..."
    echo "" >> .env
    echo "# Phase 2: 模型热度追踪（生产环境谨慎启用，需评估 DB 负载）" >> .env
    echo "LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false" >> .env
fi

# 启动服务
echo "  → 启动服务..."
systemctl start llm-gateway || supervisorctl start llm-gateway || nohup ./llm-gateway-go > logs/gateway.log 2>&1 &
sleep 3

# 检查启动状态
echo "  → 检查启动状态..."
if pgrep -f llm-gateway-go > /dev/null; then
    echo "  ✓ 服务启动成功"
    echo ""
    echo "  当前版本信息:"
    ./llm-gateway-go --version 2>/dev/null || echo "    (无版本信息)"
    echo ""
    echo "  进程信息:"
    ps aux | grep llm-gateway-go | grep -v grep | head -2
else
    echo "  ✗ 服务启动失败"
    echo "  查看日志: tail -50 /data/services/llm-gateway-go/logs/gateway.log"
    exit 1
fi

REMOTE_SCRIPT

echo ""
echo "=== 部署完成 ==="
echo ""
echo "下一步："
echo "1. 验证服务: ssh $SERVER 'systemctl status llm-gateway'"
echo "2. 查看日志: ssh $SERVER 'tail -f $REMOTE_DIR/logs/gateway.log'"
echo "3. 检查数据库索引: 运行 sql/create_request_logs_index.sql"
echo "4. 启用热度追踪: 修改 .env 中 LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true"
echo "5. 重启服务: ssh $SERVER 'systemctl restart llm-gateway'"
