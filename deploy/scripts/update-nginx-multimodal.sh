#!/bin/bash
# update-nginx-multimodal.sh
# 更新 Nginx 配置以支持多模态大请求（256MB）
# 
# 使用方法:
#   ./update-nginx-multimodal.sh 154 /etc/nginx/conf.d/llm-kxpms-cn.conf
#   ./update-nginx-multimodal.sh 252 /etc/nginx/conf.d/kxpms-on-252.conf
#
# 作者: LLM Gateway Team
# 日期: 2026-07-25

set -e

SERVER_IP="${1}"
CONFIG_FILE="${2}"
BACKUP_DIR="/etc/nginx/conf.d/backups"
REQUIRED_SIZE="256m"

if [ -z "$SERVER_IP" ] || [ -z "$CONFIG_FILE" ]; then
  echo "❌ 用法: $0 <server_ip> <config_file>"
  echo ""
  echo "示例:"
  echo "  $0 154 /etc/nginx/conf.d/llm-kxpms-cn.conf"
  echo "  $0 252 /etc/nginx/conf.d/kxpms-on-252.conf"
  echo "  $0 245 /etc/nginx/conf.d/llmgo-245.conf"
  exit 1
fi

echo "🚀 Nginx 多模态配置更新脚本"
echo "================================"
echo "服务器: $SERVER_IP"
echo "配置文件: $CONFIG_FILE"
echo "目标大小: $REQUIRED_SIZE"
echo ""

# 检查 SSH 连接
echo "1️⃣ 检查 SSH 连接..."
if ! ssh -o ConnectTimeout=5 root@$SERVER_IP "echo '✅ SSH 连接成功'" 2>/dev/null; then
  echo "❌ 无法连接到 root@$SERVER_IP"
  echo "请检查:"
  echo "  - SSH 密钥是否配置"
  echo "  - 服务器是否在线"
  echo "  - 防火墙规则"
  exit 1
fi

# 检查配置文件是否存在
echo ""
echo "2️⃣ 检查配置文件..."
if ! ssh root@$SERVER_IP "test -f $CONFIG_FILE"; then
  echo "❌ 配置文件不存在: $CONFIG_FILE"
  echo ""
  echo "可用的配置文件:"
  ssh root@$SERVER_IP "ls -1 /etc/nginx/conf.d/*.conf"
  exit 1
fi
echo "✅ 配置文件存在"

# 备份现有配置
echo ""
echo "3️⃣ 备份现有配置..."
BACKUP_FILE="$BACKUP_DIR/$(basename $CONFIG_FILE).$(date +%Y%m%d_%H%M%S).bak"
ssh root@$SERVER_IP "mkdir -p $BACKUP_DIR && cp $CONFIG_FILE $BACKUP_FILE"
echo "✅ 备份完成: $BACKUP_FILE"

# 检查现有配置
echo ""
echo "4️⃣ 检查现有 client_max_body_size 配置..."
EXISTING_CONFIG=$(ssh root@$SERVER_IP "grep 'client_max_body_size' $CONFIG_FILE || echo 'NOT_FOUND'")

if [ "$EXISTING_CONFIG" != "NOT_FOUND" ]; then
  echo "📋 当前配置:"
  echo "$EXISTING_CONFIG"
  echo ""
  
  # 检查是否已经是 256m 或更大
  if echo "$EXISTING_CONFIG" | grep -q "256m\|512m\|1024m"; then
    echo "✅ 配置已满足要求（>=256m），无需更新"
    exit 0
  fi
  
  echo "⚠️  当前配置不满足要求（需要 256m）"
  read -p "是否更新配置？(y/N) " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "❌ 用户取消操作"
    exit 0
  fi
  
  # 替换现有配置
  echo "🔧 更新现有配置..."
  ssh root@$SERVER_IP "sed -i 's/client_max_body_size [^;]*;/client_max_body_size $REQUIRED_SIZE;/' $CONFIG_FILE"
else
  echo "📝 未找到 client_max_body_size 配置，将添加新配置"
  
  # 在 ssl_certificate_key 之后添加配置
  if ssh root@$SERVER_IP "grep -q 'ssl_certificate_key' $CONFIG_FILE"; then
    echo "🔧 在 SSL 配置后添加..."
    ssh root@$SERVER_IP "sed -i '/ssl_certificate_key/a\\    \\n    # 多模态支持：256MB 请求体限制 (2026-07-25)\\n    client_max_body_size $REQUIRED_SIZE;' $CONFIG_FILE"
  else
    # 如果没有 SSL 配置，在 server_name 后添加
    echo "🔧 在 server_name 后添加..."
    ssh root@$SERVER_IP "sed -i '/server_name/a\\    \\n    # 多模态支持：256MB 请求体限制 (2026-07-25)\\n    client_max_body_size $REQUIRED_SIZE;' $CONFIG_FILE"
  fi
fi

# 显示更新后的配置
echo ""
echo "5️⃣ 验证更新后的配置..."
UPDATED_CONFIG=$(ssh root@$SERVER_IP "grep -A 1 -B 1 'client_max_body_size' $CONFIG_FILE")
echo "📋 更新后的配置:"
echo "$UPDATED_CONFIG"

# 测试 Nginx 配置语法
echo ""
echo "6️⃣ 测试 Nginx 配置语法..."
if ! ssh root@$SERVER_IP "nginx -t" 2>&1; then
  echo ""
  echo "❌ Nginx 配置测试失败！"
  echo ""
  echo "正在恢复备份..."
  ssh root@$SERVER_IP "cp $BACKUP_FILE $CONFIG_FILE"
  echo "✅ 已恢复到备份版本"
  exit 1
fi
echo "✅ Nginx 配置语法正确"

# 重载 Nginx
echo ""
echo "7️⃣ 重载 Nginx..."
if ssh root@$SERVER_IP "nginx -s reload" 2>&1; then
  echo "✅ Nginx 重载成功"
else
  echo "❌ Nginx 重载失败"
  echo "请手动检查服务状态: systemctl status nginx"
  exit 1
fi

# 最终验证
echo ""
echo "8️⃣ 最终验证..."
FINAL_CONFIG=$(ssh root@$SERVER_IP "nginx -T 2>&1 | grep 'client_max_body_size' | head -5")
echo "📋 生效的配置:"
echo "$FINAL_CONFIG"

echo ""
echo "✅ ================================"
echo "✅ 配置更新完成！"
echo "✅ ================================"
echo ""
echo "📊 建议测试:"
echo "  1. 查看看板统计: https://llm.kxpms.cn/dashboard"
echo "  2. 测试大请求:"
echo "     curl -X POST https://llm.kxpms.cn/v1/chat/completions \\"
echo "       -H 'Authorization: Bearer sk-xxx' \\"
echo "       -H 'Content-Type: application/json' \\"
echo "       --data-binary @large_file.json"
echo ""
echo "📝 备份位置: $BACKUP_FILE"
