#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

HOST_245="245"
DB_CONTAINER="llm-gateway-pg"  # 根据实际情况调整

echo "========================================"
echo "  部署到245服务器并验证"
echo "========================================"
echo ""
echo "目标服务器: $HOST_245"
echo "当前时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo ""

# 步骤1: 检查SSH连接
echo -e "${BLUE}[1/8] 检查SSH连接...${NC}"
if ssh "$HOST_245" "echo '连接成功'" > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} SSH连接正常"
else
    echo -e "${RED}✗${NC} 无法连接到$HOST_245"
    echo "请检查SSH配置或网络连接"
    exit 1
fi
echo ""

# 步骤2: 检查数据库容器
echo -e "${BLUE}[2/8] 检查数据库容器...${NC}"
DB_STATUS=$(ssh "$HOST_245" "docker ps --filter name=$DB_CONTAINER --format '{{.Status}}'" 2>/dev/null || echo "")
if [[ -n "$DB_STATUS" ]]; then
    echo -e "${GREEN}✓${NC} 数据库容器运行中: $DB_STATUS"
else
    echo -e "${YELLOW}⚠${NC} 数据库容器未找到或未运行"
    echo "容器名称: $DB_CONTAINER"
    echo ""
    echo "查看所有容器:"
    ssh "$HOST_245" "docker ps --format 'table {{.Names}}\t{{.Status}}'"
    echo ""
    read -p "请输入正确的数据库容器名称 (直接回车跳过): " user_container
    if [[ -n "$user_container" ]]; then
        DB_CONTAINER="$user_container"
        echo "已更新容器名称为: $DB_CONTAINER"
    fi
fi
echo ""

# 步骤3: 备份当前数据库
echo -e "${BLUE}[3/8] 备份数据库...${NC}"
BACKUP_FILE="backup_245_$(date +%Y%m%d_%H%M%S).sql"
echo "备份文件: $BACKUP_FILE"

if ssh "$HOST_245" "docker exec $DB_CONTAINER pg_dump -U llm_gateway llm_gateway > /tmp/$BACKUP_FILE 2>&1"; then
    BACKUP_SIZE=$(ssh "$HOST_245" "du -h /tmp/$BACKUP_FILE | cut -f1")
    echo -e "${GREEN}✓${NC} 备份完成: $BACKUP_SIZE"
    
    # 下载备份到本地
    echo "下载备份到本地..."
    scp "$HOST_245:/tmp/$BACKUP_FILE" "backups/$BACKUP_FILE" 2>/dev/null || echo "  (本地备份失败，服务器上仍有备份)"
else
    echo -e "${YELLOW}⚠${NC} 备份失败或命令需要调整"
    echo ""
    echo "继续执行？(yes/no) "
    read -r response
    if [[ "$response" != "yes" ]]; then
        exit 1
    fi
fi
echo ""

# 步骤4: 上传迁移文件
echo -e "${BLUE}[4/8] 上传迁移文件...${NC}"
ssh "$HOST_245" "mkdir -p /tmp/migrations"

for migration in 1000_billing_status_and_credit_limit 1001_bill_payment_tracking 1002_billing_retry_queue; do
    echo "上传 $migration.sql..."
    scp "sql/migrations/startup/${migration}.sql" "$HOST_245:/tmp/migrations/" > /dev/null
    scp "sql/migrations/startup/${migration}.down.sql" "$HOST_245:/tmp/migrations/" > /dev/null
done

echo -e "${GREEN}✓${NC} 迁移文件已上传"
echo ""

# 步骤5: 应用迁移
echo -e "${BLUE}[5/8] 应用数据库迁移...${NC}"
echo -e "${YELLOW}⚠ 即将修改245生产数据库${NC}"
echo ""
echo "继续执行？(yes/no) "
read -r response

if [[ "$response" != "yes" ]]; then
    echo "操作已取消"
    exit 0
fi

echo ""
echo "应用迁移1000..."
ssh "$HOST_245" "docker exec -i $DB_CONTAINER psql -U llm_gateway -d llm_gateway < /tmp/migrations/1000_billing_status_and_credit_limit.sql" 2>&1 | tail -5
echo -e "${GREEN}✓${NC} 迁移1000完成"
echo ""

echo "应用迁移1001..."
ssh "$HOST_245" "docker exec -i $DB_CONTAINER psql -U llm_gateway -d llm_gateway < /tmp/migrations/1001_bill_payment_tracking.sql" 2>&1 | tail -5
echo -e "${GREEN}✓${NC} 迁移1001完成"
echo ""

echo "应用迁移1002..."
ssh "$HOST_245" "docker exec -i $DB_CONTAINER psql -U llm_gateway -d llm_gateway < /tmp/migrations/1002_billing_retry_queue.sql" 2>&1 | tail -5
echo -e "${GREEN}✓${NC} 迁移1002完成"
echo ""

# 步骤6: 验证迁移结果
echo -e "${BLUE}[6/8] 验证迁移结果...${NC}"

echo "检查新表..."
ssh "$HOST_245" "docker exec $DB_CONTAINER psql -U llm_gateway -d llm_gateway -c \"
SELECT table_name FROM information_schema.tables 
WHERE table_name IN ('billing_precheck_log', 'billing_retry_queue', 'maas_bill_status_audit')
ORDER BY table_name;
\""

echo ""
echo "检查新字段..."
ssh "$HOST_245" "docker exec $DB_CONTAINER psql -U llm_gateway -d llm_gateway -c \"
SELECT column_name FROM information_schema.columns 
WHERE table_name = 'maas_monthly_bills' 
  AND column_name IN ('paid_at', 'payment_method')
ORDER BY column_name;
\""

echo -e "${GREEN}✓${NC} 验证完成"
echo ""

# 步骤7: 编译并部署服务
echo -e "${BLUE}[7/8] 编译并部署服务...${NC}"

echo "编译服务..."
if go build -o llm-gateway ./cmd/gateway 2>&1 | grep -v "warning:"; then
    echo -e "${GREEN}✓${NC} 编译成功"
else
    echo -e "${RED}✗${NC} 编译失败"
    exit 1
fi

echo ""
echo "上传二进制到245..."
scp llm-gateway "$HOST_245:/tmp/llm-gateway-new"
echo -e "${GREEN}✓${NC} 上传完成"

echo ""
echo -e "${YELLOW}⚠ 即将重启245服务${NC}"
echo "继续？(yes/no) "
read -r response

if [[ "$response" != "yes" ]]; then
    echo "部署已取消（迁移已应用，服务未重启）"
    exit 0
fi

echo ""
echo "备份旧二进制..."
ssh "$HOST_245" "cp /opt/llm-gateway/llm-gateway /opt/llm-gateway/llm-gateway.backup.$(date +%Y%m%d_%H%M%S) 2>/dev/null || true"

echo "部署新二进制..."
ssh "$HOST_245" "mv /tmp/llm-gateway-new /opt/llm-gateway/llm-gateway && chmod +x /opt/llm-gateway/llm-gateway"

echo "重启服务..."
ssh "$HOST_245" "sudo systemctl restart llm-gateway"

sleep 3
echo ""
echo "检查服务状态..."
ssh "$HOST_245" "sudo systemctl status llm-gateway --no-pager | head -15"

echo -e "${GREEN}✓${NC} 服务已重启"
echo ""

# 步骤8: 功能测试
echo -e "${BLUE}[8/8] 功能测试...${NC}"

echo "测试健康检查..."
if ssh "$HOST_245" "curl -s http://localhost:8080/api/system/version" | grep -q "version"; then
    echo -e "${GREEN}✓${NC} 健康检查通过"
else
    echo -e "${YELLOW}⚠${NC} 健康检查失败，请检查服务日志"
fi

echo ""
echo "检查日志中的预检消息..."
ssh "$HOST_245" "tail -50 /var/log/llm-gateway/app.log 2>/dev/null | grep 'billing precheck' || echo '(暂无预检日志)'"

echo ""
echo "========================================"
echo -e "${GREEN}✅ 部署完成${NC}"
echo "========================================"
echo ""
echo "部署总结:"
echo "  ✓ 数据库迁移: 已应用"
echo "  ✓ 服务部署: 已更新"
echo "  ✓ 备份文件: /tmp/$BACKUP_FILE (245服务器)"
echo ""
echo "后续步骤:"
echo "  1. 观察日志: ssh $HOST_245 'tail -f /var/log/llm-gateway/app.log'"
echo "  2. 监控指标: billing_precheck_rejected_total"
echo "  3. 检查数据: SELECT COUNT(*) FROM billing_precheck_log;"
echo ""
echo "如需回滚:"
echo "  1. ssh $HOST_245 'sudo systemctl stop llm-gateway'"
echo "  2. ssh $HOST_245 'mv /opt/llm-gateway/llm-gateway.backup.* /opt/llm-gateway/llm-gateway'"
echo "  3. 回滚数据库 (使用down迁移)"
echo ""
