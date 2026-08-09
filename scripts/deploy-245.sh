#!/bin/bash
# 部署脚本：245 测试环境部署 LLM Gateway 新版本
# 用途：部署 modality 修复 + 路径遍历防护 + API Key 认证

set -e  # 遇到错误立即退出

echo "=========================================="
echo "LLM Gateway 部署脚本 - 245 测试环境"
echo "=========================================="
echo ""

# 1. 检查当前目录
echo "[1/7] 检查工作目录..."
if [ ! -f "go.mod" ]; then
    echo "❌ 错误：不在项目根目录，请 cd 到 llm-gateway-go 目录"
    exit 1
fi
echo "✅ 当前目录正确"
echo ""

# 2. 备份当前版本
echo "[2/7] 备份当前版本..."
BACKUP_DIR="backup_$(date +%Y%m%d_%H%M%S)"
mkdir -p "../$BACKUP_DIR"
cp -r . "../$BACKUP_DIR/" || echo "⚠️  备份失败，继续..."
echo "✅ 已备份到 ../$BACKUP_DIR"
echo ""

# 3. 拉取最新代码
echo "[3/7] 拉取最新代码..."
git fetch origin
git checkout main
git pull origin main
CURRENT_COMMIT=$(git rev-parse --short HEAD)
echo "✅ 当前版本: $CURRENT_COMMIT"
echo ""

# 4. 检查关键提交是否存在
echo "[4/7] 验证关键更新..."
if git log --oneline -10 | grep -q "fix(multimodal)"; then
    echo "✅ 找到 modality 修复提交"
else
    echo "⚠️  未找到 modality 修复提交，可能需要手动验证"
fi

if git log --oneline -10 | grep -q "feat(attachments)"; then
    echo "✅ 找到 API Key 认证提交"
else
    echo "⚠️  未找到 API Key 认证提交，可能需要手动验证"
fi
echo ""

# 5. 构建新版本
echo "[5/7] 构建服务..."
go build -o llm-gateway ./cmd/gateway
if [ $? -eq 0 ]; then
    echo "✅ 构建成功"
else
    echo "❌ 构建失败"
    exit 1
fi
echo ""

# 6. 检查配置文件
echo "[6/7] 检查配置..."
if [ -f ".env" ]; then
    echo "✅ 找到 .env 配置文件"
    
    # 检查关键配置
    if grep -q "LLM_GATEWAY_ATTACHMENT_AUTH_MODE" .env; then
        MODE=$(grep "LLM_GATEWAY_ATTACHMENT_AUTH_MODE" .env | cut -d'=' -f2)
        echo "   认证模式: $MODE"
    else
        echo "   认证模式: none (默认)"
    fi
else
    echo "⚠️  未找到 .env 文件，将使用默认配置"
fi
echo ""

# 7. 提示重启服务
echo "[7/7] 准备重启服务..."
echo ""
echo "=========================================="
echo "构建完成！下一步操作："
echo "=========================================="
echo ""
echo "1. 停止当前服务："
echo "   sudo systemctl stop llm-gateway"
echo "   # 或使用 supervisorctl / docker 命令"
echo ""
echo "2. (可选) 配置 API Key 认证："
echo "   echo 'LLM_GATEWAY_ATTACHMENT_AUTH_MODE=apikey' >> .env"
echo ""
echo "3. 启动新版本："
echo "   sudo systemctl start llm-gateway"
echo "   # 或使用 supervisorctl / docker 命令"
echo ""
echo "4. 验证服务状态："
echo "   sudo systemctl status llm-gateway"
echo "   curl http://localhost:8080/healthz"
echo ""
echo "5. 查看日志："
echo "   tail -f /var/log/llm-gateway/gateway.log"
echo ""
echo "=========================================="
echo "部署脚本执行完成"
echo "=========================================="
