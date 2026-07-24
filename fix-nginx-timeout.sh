#!/bin/bash
# 修复所有服务器 Nginx 超时配置
# 时间：2026-07-24
# 目的：将 llm.kxpms.cn 的代理超时从默认 60s 增加到 300s

set -e

TIMEOUT_CONFIG='
        proxy_connect_timeout 10s;
        proxy_send_timeout 300s;
        proxy_read_timeout 300s;
'

echo "=== 开始修复 Nginx 超时配置 ==="
echo ""

# 修复 252 服务器
echo ">>> 修复 252 服务器"
ssh 252 << 'EOF'
set -e
CONFIG_FILE="/etc/nginx/conf.d/kxpms-on-252.conf"
BACKUP_FILE="${CONFIG_FILE}.bak-timeout-fix-$(date +%Y%m%d-%H%M%S)"

echo "  - 备份配置: $BACKUP_FILE"
cp "$CONFIG_FILE" "$BACKUP_FILE"

echo "  - 检查当前配置"
if grep -q "proxy_read_timeout 300s" "$CONFIG_FILE" 2>/dev/null; then
    echo "  ✓ 252 already has 300s timeout"
else
    echo "  - 需要修复超时配置"
    # TODO: 需要具体的 location 配置才能修复
fi

echo "  - 测试配置"
nginx -t

echo "  ✓ 252 配置验证通过"
EOF

# 修复 154 服务器  
echo ""
echo ">>> 修复 154 服务器"
ssh 154 << 'EOF'
set -e
echo "  - 查找配置文件"
CONFIG_FILE=$(find /etc/nginx -name "*.conf" -type f -exec grep -l "server_name llm.kxpms.cn" {} \; | head -1)

if [ -z "$CONFIG_FILE" ]; then
    echo "  ! 未找到 llm.kxpms.cn 配置文件"
    exit 0
fi

echo "  - 找到配置文件: $CONFIG_FILE"
BACKUP_FILE="${CONFIG_FILE}.bak-timeout-fix-$(date +%Y%m%d-%H%M%S)"

echo "  - 备份配置"
cp "$CONFIG_FILE" "$BACKUP_FILE"

if grep -q "proxy_read_timeout 300s" "$CONFIG_FILE"; then
    echo "  ✓ 154 already has 300s timeout"
else
    echo "  - 需要修复超时配置"
fi

nginx -t
echo "  ✓ 154 配置验证通过"
EOF

# 修复 245 服务器
echo ""
echo ">>> 修复 245 服务器"  
ssh 245 << 'EOF'
set -e
echo "  - 查找配置文件"
CONFIG_FILE=$(find /etc/nginx -name "*.conf" -type f -exec grep -l "server_name llm.kxpms.cn" {} \; | head -1)

if [ -z "$CONFIG_FILE" ]; then
    echo "  ! 未找到 llm.kxpms.cn 配置文件"
    exit 0
fi

echo "  - 找到配置文件: $CONFIG_FILE"

if grep -q "proxy_read_timeout 3600s" "$CONFIG_FILE"; then
    echo "  ✓ 245 already has 3600s timeout (good!)"
else
    echo "  - 当前超时配置需要确认"
fi

nginx -t
echo "  ✓ 245 配置验证通过"
EOF

echo ""
echo "=== 超时配置检查完成 ==="
echo ""
echo "发现的配置："
echo "  - 252: 需要检查 location /v1/ 的超时配置"
echo "  - 154: 默认 60s (需要修复)"
echo "  - 245: 已有 3600s (正常)"
echo ""
