#!/bin/bash
# deploy-tab-navigation.sh — 部署 Tab 导航功能到 154
# 
# 此脚本仅部署前端 web/dist，不涉及后端 binary
# 
# 前置条件：
#   1. web/dist 已 build (pnpm run build)
#   2. export SSHPASS='Kaixuan2026&#*9527'
#   3. 154 SSH 端口 25022 可达

set -euo pipefail

REMOTE_HOST="47.97.111.154"
REMOTE_PORT="25022"
REMOTE_USER="root"
REMOTE_DIR="/opt/llm-gateway-go"
SERVICE_NAME="llm-gateway-go.service"

echo "=== [1/5] 检查前置条件 ==="
if [ -z "${SSHPASS:-}" ]; then
  echo "❌ SSHPASS 未设置"
  echo "   export SSHPASS='Kaixuan2026&#*9527'"
  exit 1
fi

if [ ! -d "web/dist" ]; then
  echo "❌ web/dist 不存在，请先 build"
  echo "   cd web && pnpm run build"
  exit 1
fi

if ! command -v sshpass &>/dev/null; then
  echo "❌ sshpass 未安装"
  echo "   brew install sshpass"
  exit 1
fi

echo "✓ 前置条件满足"

echo ""
echo "=== [2/5] 打包 web/dist ==="
TARBALL="/tmp/web-dist-$(date +%Y%m%d_%H%M%S).tar.gz"
tar -czf "$TARBALL" -C web/dist .
TAR_SIZE=$(du -h "$TARBALL" | awk '{print $1}')
echo "✓ 打包完成: $TARBALL ($TAR_SIZE)"

echo ""
echo "=== [3/5] 上传到 154 ==="
sshpass -e scp -P "$REMOTE_PORT" -o StrictHostKeyChecking=no "$TARBALL" "$REMOTE_USER@$REMOTE_HOST:/tmp/"
echo "✓ 上传完成"

echo ""
echo "=== [4/5] 部署到 $REMOTE_DIR/web ==="
sshpass -e ssh -p "$REMOTE_PORT" -o StrictHostKeyChecking=no "$REMOTE_USER@$REMOTE_HOST" bash <<'REMOTE_SCRIPT'
set -e
cd /opt/llm-gateway-go

# 备份旧 web
BACKUP_DIR="web.bak.$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"
if [ -d web ] && [ "$(ls -A web 2>/dev/null)" ]; then
  cp -r web/* "$BACKUP_DIR/" 2>/dev/null || true
  echo "✓ 备份到 $BACKUP_DIR"
fi

# 解压新 dist
rm -rf web/*
TARBALL=$(ls -t /tmp/web-dist-*.tar.gz | head -1)
tar -xzf "$TARBALL" -C web/
echo "✓ 解压 $TARBALL"

# 验证文件
FILE_COUNT=$(find web -type f | wc -l)
echo "✓ 共 $FILE_COUNT 个文件"
ls -lh web/index.html 2>/dev/null || echo "⚠ index.html 不存在"
REMOTE_SCRIPT

echo ""
echo "=== [5/5] 重启服务 ==="
sshpass -e ssh -p "$REMOTE_PORT" -o StrictHostKeyChecking=no "$REMOTE_USER@$REMOTE_HOST" \
  "systemctl restart $SERVICE_NAME && sleep 3 && systemctl status $SERVICE_NAME --no-pager | head -10"

echo ""
echo "✅ 部署完成"
echo ""
echo "验证步骤："
echo "  1. curl https://llm.kxpms.cn/healthz"
echo "  2. 浏览器访问 https://llm.kxpms.cn"
echo "  3. 仪表盘应显示两个 Tab：'实时请求流' 和 '会话与统计'"
echo "  4. 切换 Tab，验证 SessionStatsPanel 仅在 '会话与统计' Tab 显示"
echo "  5. 强制刷新 (Ctrl+Shift+R) 确保加载新 chunk"
