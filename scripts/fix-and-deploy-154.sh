#!/bin/bash
# 快速部署前端修复到 154 服务器

set -e

echo "=========================================="
echo "修复并部署前端 JS 错误到 154"
echo "=========================================="
echo ""

# 1. 重新构建前端
echo "步骤 1: 重新构建前端..."
cd web
npm run build
cd ..
echo "✅ 前端构建完成"
echo ""

# 2. 运行本地测试
echo "步骤 2: 运行本地验证..."
if [ -f "scripts/test-frontend-fix.sh" ]; then
    ./scripts/test-frontend-fix.sh || {
        echo "❌ 本地测试失败，请先修复问题"
        exit 1
    }
    echo ""
fi

# 3. 打包部署文件
echo "步骤 3: 创建部署包..."
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
DEPLOY_PKG="frontend-fix-$TIMESTAMP.tar.gz"

tar -czf "/tmp/$DEPLOY_PKG" web/dist

echo "✅ 部署包创建成功: /tmp/$DEPLOY_PKG"
echo ""

# 4. 显示后续步骤
echo "=========================================="
echo "部署到 154 服务器的步骤："
echo "=========================================="
echo ""
echo "1. 上传到服务器："
echo "   scp -P 25022 /tmp/$DEPLOY_PKG root@47.97.111.154:/tmp/"
echo ""
echo "2. SSH 登录服务器并执行："
echo "   ssh -p 25022 root@47.97.111.154"
echo "   cd /root/llm-gateway-go  # 或你的实际路径"
echo ""
echo "3. 备份并部署："
echo "   # 备份当前版本"
echo "   tar -czf web-dist-backup-$TIMESTAMP.tar.gz web/dist"
echo ""
echo "   # 部署新版本"
echo "   rm -rf web/dist/*"
echo "   tar -xzf /tmp/$DEPLOY_PKG --strip-components=1 -C ./"
echo ""
echo "   # 验证部署"
echo "   ls -la web/dist/index.html"
echo "   curl -s http://localhost:8080/ | head -10"
echo ""
echo "4. 如果 gateway 使用了嵌入式静态文件，需要重新编译并重启"
echo "   # 或者如果是通过文件系统提供，只需刷新浏览器"
echo ""
echo "5. 浏览器验证："
echo "   - 访问 https://llm.kxpms.cn"
echo "   - 按 F12 打开开发者工具"
echo "   - 刷新页面，检查控制台"
echo "   - 确认不再有 'Cannot destructure property row' 错误"
echo ""

echo "部署包位置: /tmp/$DEPLOY_PKG"
echo ""
