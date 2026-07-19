#!/usr/bin/env bash
# 部署质量评分服务到 245 测试环境

set -euo pipefail

# 配置
SERVER="root@8.136.114.245"
PORT="25022"
REMOTE_DIR="/opt/llm-gateway-go"
SERVICE_NAME="quality-service"
SERVICE_PORT="8081"

echo "=== 部署质量评分服务到 245 ==="
echo ""

# 1. 本地编译
echo "1. 编译服务..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/${SERVICE_NAME}-linux-amd64 ./cmd/quality-service
echo "✅ 编译完成 ($(du -h bin/${SERVICE_NAME}-linux-amd64 | cut -f1))"
echo ""

# 2. 上传到服务器
echo "2. 上传到服务器..."
ssh -p ${PORT} ${SERVER} "mkdir -p ${REMOTE_DIR}/bin ${REMOTE_DIR}/logs"
scp -P ${PORT} bin/${SERVICE_NAME}-linux-amd64 ${SERVER}:${REMOTE_DIR}/bin/${SERVICE_NAME}
echo "✅ 上传完成"
echo ""

# 3. 创建 systemd 服务
echo "3. 创建 systemd 服务..."
cat > /tmp/${SERVICE_NAME}.service << 'SYSTEMD'
[Unit]
Description=LLM Gateway Quality Service
After=network.target postgresql.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/llm-gateway-go
Environment="DATABASE_URL=postgres://llmuser:llmpass@localhost:4100/llm_gateway?sslmode=disable"
ExecStart=/opt/llm-gateway-go/bin/quality-service -http=:8081 -scheduler=true -interval=5m
Restart=always
RestartSec=10

StandardOutput=append:/opt/llm-gateway-go/logs/quality-service.log
StandardError=append:/opt/llm-gateway-go/logs/quality-service.error.log

[Install]
WantedBy=multi-user.target
SYSTEMD

scp -P ${PORT} /tmp/${SERVICE_NAME}.service ${SERVER}:/etc/systemd/system/
ssh -p ${PORT} ${SERVER} "systemctl daemon-reload"
echo "✅ systemd 服务创建完成"
echo ""

# 4. 启动服务
echo "4. 启动服务..."
ssh -p ${PORT} ${SERVER} "systemctl stop ${SERVICE_NAME} 2>/dev/null || true"
ssh -p ${PORT} ${SERVER} "systemctl start ${SERVICE_NAME}"
ssh -p ${PORT} ${SERVER} "systemctl enable ${SERVICE_NAME}"
echo "✅ 服务已启动"
echo ""

# 5. 等待服务就绪
echo "5. 等待服务就绪..."
sleep 3
echo ""

# 6. 健康检查
echo "6. 健康检查..."
if ssh -p ${PORT} ${SERVER} "curl -f -s http://localhost:${SERVICE_PORT}/health" > /dev/null; then
    echo "✅ 健康检查通过"
else
    echo "❌ 健康检查失败"
    ssh -p ${PORT} ${SERVER} "systemctl status ${SERVICE_NAME}"
    exit 1
fi
echo ""

# 7. 查看服务状态
echo "7. 服务状态..."
ssh -p ${PORT} ${SERVER} "systemctl status ${SERVICE_NAME} --no-pager"
echo ""

# 8. 查看最近日志
echo "8. 最近日志..."
ssh -p ${PORT} ${SERVER} "tail -20 /opt/llm-gateway-go/logs/quality-service.log"
echo ""

echo "=== 部署完成 ==="
echo ""
echo "服务信息:"
echo "  地址: http://8.136.114.245:${SERVICE_PORT}"
echo "  健康检查: curl http://8.136.114.245:${SERVICE_PORT}/health"
echo "  查询画像: curl 'http://8.136.114.245:${SERVICE_PORT}/api/quality/profile?provider_id=587&model=claude-3-5-sonnet-20241022'"
echo "  查看日志: ssh -p ${PORT} ${SERVER} 'tail -f /opt/llm-gateway-go/logs/quality-service.log'"
echo "  重启服务: ssh -p ${PORT} ${SERVER} 'systemctl restart ${SERVICE_NAME}'"
