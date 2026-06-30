#!/bin/bash
set -e

# Phase 2 热度追踪功能部署脚本（184测试环境）
# 实际服务器: 14.103.112.184:25022 (SSH别名: 184)
# 项目路径: /opt/kx-memora-build/services/llm-gateway-go

SERVER="184"
REMOTE_DIR="/opt/kx-memora-build/services/llm-gateway-go"
BACKUP_DIR="/opt/kx-memora-build/backups/llm-gateway-go"

echo "=== Phase 2 热度追踪功能部署 (184测试环境) ==="
echo "服务器: $SERVER (14.103.112.184:25022)"
echo "目录: $REMOTE_DIR"
echo ""

# 0. 检查SSH连接
echo "0. 检查SSH连接..."
if ! ssh $SERVER "echo 'OK'" >/dev/null 2>&1; then
    echo "✗ SSH连接失败，请检查："
    echo "  1. 密钥是否加载: ssh-add ~/.ssh/184_id_rsa"
    echo "  2. 网络是否通畅"
    exit 1
fi
echo "✓ SSH连接正常"
echo ""

# 1. 本地构建
echo "1. 本地构建二进制..."
GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux-amd64 ./cmd/gateway/
if [ ! -f llm-gateway-go-linux-amd64 ]; then
    echo "✗ 构建失败"
    exit 1
fi
FILE_SIZE=$(ls -lh llm-gateway-go-linux-amd64 | awk '{print $5}')
echo "✓ 构建成功: $FILE_SIZE"
echo ""

# 2. 上传到服务器
echo "2. 上传到 184 服务器..."
scp llm-gateway-go-linux-amd64 $SERVER:/tmp/llm-gateway-go-new
echo "✓ 上传完成"
echo ""

# 3. 远程部署
echo "3. 远程部署并重启..."
ssh $SERVER bash << REMOTE_SCRIPT
set -e
cd $REMOTE_DIR

echo "  → 检查项目目录..."
if [ ! -d "$REMOTE_DIR" ]; then
    echo "  ✗ 项目目录不存在: $REMOTE_DIR"
    exit 1
fi

echo "  → 备份当前版本..."
mkdir -p $BACKUP_DIR
if [ -f llm-gateway-go ]; then
    cp llm-gateway-go $BACKUP_DIR/llm-gateway-go.\$(date +%Y%m%d_%H%M%S)
    echo "  ✓ 备份完成"
else
    echo "  ⚠ 当前无二进制文件，跳过备份"
fi

echo "  → 停止服务..."
# 尝试多种停止方式
systemctl stop llm-gateway 2>/dev/null || \
supervisorctl stop llm-gateway 2>/dev/null || \
pkill -f llm-gateway-go 2>/dev/null || \
true
sleep 2

echo "  → 替换二进制..."
mv /tmp/llm-gateway-go-new llm-gateway-go
chmod +x llm-gateway-go

echo "  → 检查 .env 配置..."
if [ ! -f .env ]; then
    echo "  ✗ .env 文件不存在"
    exit 1
fi

if ! grep -q "LLM_GATEWAY_ENABLE_POPULARITY_TRACKING" .env 2>/dev/null; then
    echo "  → 添加热度追踪配置（默认禁用）..."
    echo "" >> .env
    echo "# Phase 2: 模型热度追踪（生产环境谨慎启用，需评估 DB 负载）" >> .env
    echo "LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=false" >> .env
    echo "  ✓ 配置已添加"
else
    echo "  ✓ 热度追踪配置已存在"
fi

echo "  → 启动服务..."
# 尝试多种启动方式
if systemctl start llm-gateway 2>/dev/null; then
    echo "  ✓ systemd 启动成功"
elif supervisorctl start llm-gateway 2>/dev/null; then
    echo "  ✓ supervisord 启动成功"
else
    echo "  → 使用 nohup 后台启动..."
    mkdir -p logs
    nohup ./llm-gateway-go > logs/gateway.log 2>&1 &
    echo "  ✓ nohup 启动成功"
fi

sleep 3

echo "  → 检查启动状态..."
if pgrep -f llm-gateway-go > /dev/null; then
    echo "  ✓ 服务启动成功"
    echo ""
    echo "  进程信息:"
    ps aux | grep llm-gateway-go | grep -v grep | head -2
    echo ""
    echo "  启动日志（最后10行）:"
    tail -10 logs/gateway.log 2>/dev/null || tail -10 /var/log/llm-gateway.log 2>/dev/null || echo "    (无日志文件)"
else
    echo "  ✗ 服务启动失败"
    echo "  查看日志: tail -50 $REMOTE_DIR/logs/gateway.log"
    exit 1
fi

REMOTE_SCRIPT

echo ""
echo "=== 部署完成 ==="
echo ""
echo "下一步验证："
echo "1. 查看日志: ssh 184 'tail -f $REMOTE_DIR/logs/gateway.log'"
echo "2. 检查状态: ssh 184 'ps aux | grep llm-gateway'"
echo "3. 健康检查: ssh 184 'curl -s http://localhost:8080/health'"
echo "4. 数据库准备: scp sql/phase2_db_setup.sql 184:/tmp/ && ssh 184"
echo ""
echo "启用热度追踪（可选）："
echo "  ssh 184"
echo "  cd $REMOTE_DIR"
echo "  sed -i 's/POPULARITY_TRACKING=false/POPULARITY_TRACKING=true/' .env"
echo "  systemctl restart llm-gateway"
