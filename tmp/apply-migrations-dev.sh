#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "========================================"
echo "  Dev环境数据库迁移"
echo "========================================"
echo ""

# 配置检查
echo "请确认dev环境配置："
echo ""
echo "数据库主机: ${DB_HOST:-未设置}"
echo "数据库端口: ${DB_PORT:-5432}"
echo "数据库名称: ${DB_NAME:-llm_gateway_dev}"
echo "数据库用户: ${DB_USER:-未设置}"
echo ""

if [[ -z "${DB_HOST:-}" ]] || [[ -z "${DB_USER:-}" ]]; then
    echo -e "${YELLOW}⚠ 环境变量未设置${NC}"
    echo ""
    echo "请设置以下环境变量："
    echo "  export DB_HOST='dev-server-host'"
    echo "  export DB_PORT='5432'"
    echo "  export DB_NAME='llm_gateway_dev'"
    echo "  export DB_USER='admin'"
    echo "  export PGPASSWORD='[REDACTED:secret]'"
    echo ""
    echo "或者使用 .pgpass 文件"
    echo ""
    exit 1
fi

# 询问确认
echo -e "${YELLOW}警告: 即将修改dev环境数据库${NC}"
echo ""
echo "此操作将："
echo "  1. 添加新表: billing_precheck_log, billing_retry_queue"
echo "  2. 修改表: tenant_credit_wallets, maas_monthly_bills"
echo "  3. 创建视图和触发器"
echo ""
echo "继续执行？(yes/no) "
read -r response

if [[ "$response" != "yes" ]]; then
    echo "操作已取消"
    exit 0
fi

echo ""
echo -e "${BLUE}开始应用迁移...${NC}"
echo ""

# 测试连接
echo "测试数据库连接..."
if psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "$DB_USER" -d "$DB_NAME" -c "SELECT version();" > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} 数据库连接成功"
else
    echo -e "${RED}✗${NC} 无法连接数据库"
    echo ""
    echo "请检查："
    echo "  1. 数据库服务是否运行"
    echo "  2. 主机名和端口是否正确"
    echo "  3. 用户名和密码是否正确"
    echo "  4. 网络连接是否正常"
    exit 1
fi
echo ""

# 备份建议
echo -e "${YELLOW}建议: 执行迁移前先备份数据库${NC}"
echo ""
echo "备份命令："
echo "  pg_dump -h $DB_HOST -p ${DB_PORT:-5432} -U $DB_USER $DB_NAME > backup_\$(date +%Y%m%d_%H%M%S).sql"
echo ""
echo "已经备份了吗？(yes/no) "
read -r backup_response

if [[ "$backup_response" != "yes" ]]; then
    echo -e "${YELLOW}请先备份数据库，然后重新运行此脚本${NC}"
    exit 0
fi

echo ""

# 应用迁移1
echo -e "${BLUE}[1/3] 应用 1000_billing_status_and_credit_limit.sql${NC}"
if psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1000_billing_status_and_credit_limit.sql 2>&1 | tail -5; then
    echo -e "${GREEN}✓${NC} 迁移1000完成"
else
    echo -e "${RED}✗${NC} 迁移1000失败"
    exit 1
fi
echo ""

# 应用迁移2
echo -e "${BLUE}[2/3] 应用 1001_bill_payment_tracking.sql${NC}"
if psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1001_bill_payment_tracking.sql 2>&1 | tail -5; then
    echo -e "${GREEN}✓${NC} 迁移1001完成"
else
    echo -e "${RED}✗${NC} 迁移1001失败"
    exit 1
fi
echo ""

# 应用迁移3
echo -e "${BLUE}[3/3] 应用 1002_billing_retry_queue.sql${NC}"
if psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1002_billing_retry_queue.sql 2>&1 | tail -5; then
    echo -e "${GREEN}✓${NC} 迁移1002完成"
else
    echo -e "${RED}✗${NC} 迁移1002失败"
    exit 1
fi
echo ""

echo "========================================"
echo -e "${GREEN}✅ 所有迁移已成功应用到dev环境${NC}"
echo "========================================"
echo ""
echo "下一步: 运行验证脚本"
echo "  bash tmp/verify-migrations-dev.sh"
echo ""
