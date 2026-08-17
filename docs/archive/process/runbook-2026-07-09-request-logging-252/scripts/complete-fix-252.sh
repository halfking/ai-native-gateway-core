#!/bin/bash
# 一键修复脚本 - 完整流程
# 包含所有步骤：修复数据库 → 重启服务 → 验证

set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}========================================"
echo "252数据库请求日志修复 - 完整流程"
echo -e "========================================${NC}"
echo ""

# ==================================================
# 方案选择
# ==================================================

echo "请选择执行方案："
echo ""
echo "1. 在252服务器上执行（推荐 - 无需密码）"
echo "2. 从本地远程执行（需要数据库密码）"
echo "3. 仅生成执行命令（手动复制执行）"
echo ""
read -p "请输入选项 (1/2/3): " CHOICE
echo ""

case $CHOICE in
  1)
    echo -e "${BLUE}=== 方案1: 在252服务器上执行 ===${NC}"
    echo ""
    echo "步骤1: 上传修复脚本到252服务器"
    echo "----------------------------------------"
    
    echo "执行命令:"
    echo -e "${YELLOW}scp scripts/fix-252-local.sh root@192.168.0.252:/tmp/${NC}"
    echo ""
    read -p "按Enter键执行，或Ctrl+C取消..."
    
    scp scripts/fix-252-local.sh root@192.168.0.252:/tmp/
    echo -e "${GREEN}✓ 脚本已上传${NC}"
    echo ""
    
    echo "步骤2: 在252服务器上执行修复"
    echo "----------------------------------------"
    echo "执行命令:"
    echo -e "${YELLOW}ssh root@192.168.0.252 'chmod +x /tmp/fix-252-local.sh && /tmp/fix-252-local.sh'${NC}"
    echo ""
    read -p "按Enter键执行，或Ctrl+C取消..."
    
    ssh root@192.168.0.252 'chmod +x /tmp/fix-252-local.sh && /tmp/fix-252-local.sh'
    echo ""
    ;;
    
  2)
    echo -e "${BLUE}=== 方案2: 从本地远程执行 ===${NC}"
    echo ""
    echo "需要252数据库密码"
    echo ""
    
    if [ -z "${DB_PASSWORD:-}" ]; then
        echo -n "请输入数据库密码: "
        read -s DB_PASSWORD
        export DB_PASSWORD
        echo ""
    fi
    
    echo "执行修复SQL..."
    PGPASSWORD="$DB_PASSWORD" psql -h 192.168.0.252 -U postgres -d llm_gateway \
      -f sql/fixes/fix-missing-request-wal-hot.sql
    
    echo -e "${GREEN}✓ 数据库修复完成${NC}"
    echo ""
    ;;
    
  3)
    echo -e "${BLUE}=== 方案3: 生成执行命令 ===${NC}"
    echo ""
    echo "请在252服务器上执行以下命令："
    echo ""
    echo -e "${YELLOW}# 1. 创建修复SQL文件${NC}"
    echo "cat > /tmp/fix-request-wal-hot.sql << 'EOF'"
    cat sql/fixes/fix-missing-request-wal-hot.sql
    echo "EOF"
    echo ""
    echo -e "${YELLOW}# 2. 执行修复${NC}"
    echo "sudo -u postgres psql -d llm_gateway -f /tmp/fix-request-wal-hot.sql"
    echo ""
    echo -e "${YELLOW}# 3. 清理${NC}"
    echo "rm /tmp/fix-request-wal-hot.sql"
    echo ""
    echo "复制以上命令后按Enter继续验证步骤，或Ctrl+C退出..."
    read
    ;;
    
  *)
    echo -e "${RED}无效选项${NC}"
    exit 1
    ;;
esac

# ==================================================
# 重启154服务
# ==================================================

echo ""
echo -e "${BLUE}=== 步骤: 重启154服务器上的网关 ===${NC}"
echo "----------------------------------------"
echo "执行命令:"
echo -e "${YELLOW}ssh root@192.168.0.154 'systemctl restart llm-gateway'${NC}"
echo ""
read -p "按Enter键执行，或Ctrl+C跳过..."

if ssh root@192.168.0.154 'systemctl restart llm-gateway'; then
    echo -e "${GREEN}✓ 服务重启成功${NC}"
    
    # 等待服务启动
    echo ""
    echo "等待服务启动 (5秒)..."
    sleep 5
    
    echo "检查服务状态..."
    ssh root@192.168.0.154 'systemctl status llm-gateway --no-pager | head -15'
else
    echo -e "${RED}✗ 服务重启失败${NC}"
    exit 1
fi

echo ""

# ==================================================
# 验证修复
# ==================================================

echo -e "${BLUE}=== 验证修复结果 ===${NC}"
echo "----------------------------------------"
echo ""

if [ "$CHOICE" = "2" ] || [ -n "${DB_PASSWORD:-}" ]; then
    echo "当前 request_wal_hot 表状态:"
    PGPASSWORD="${DB_PASSWORD:-}" psql -h 192.168.0.252 -U postgres -d llm_gateway -c \
      "SELECT COUNT(*) as total, MAX(created_at) as latest FROM request_wal_hot;"
else
    echo "请在252服务器上执行以下命令验证："
    echo -e "${YELLOW}sudo -u postgres psql -d llm_gateway -c \"SELECT COUNT(*) as total, MAX(created_at) as latest FROM request_wal_hot;\"${NC}"
fi

echo ""
echo -e "${BLUE}=== 后续验证步骤 ===${NC}"
echo "----------------------------------------"
echo ""
echo "1. 发送测试请求到 llm.kxpms.cn"
echo ""
echo "2. 等待1-2分钟后，检查是否有新记录："
echo ""

if [ "$CHOICE" = "2" ] || [ -n "${DB_PASSWORD:-}" ]; then
    echo -e "   ${YELLOW}PGPASSWORD=<密码> psql -h 192.168.0.252 -U postgres -d llm_gateway -c \"SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '5 minutes';\"${NC}"
else
    echo -e "   ${YELLOW}ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c \"SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '\"'\"'5 minutes'\"'\"';\"'${NC}"
fi

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}修复流程完成！${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "如果验证后发现仍无新记录，请："
echo "1. 检查154服务日志: ssh root@192.168.0.154 'journalctl -u llm-gateway -n 50'"
echo "2. 检查154的数据库配置是否指向252"
echo "3. 查看详细故障排查: cat docs/QUICK_FIX_252.md"
echo ""
