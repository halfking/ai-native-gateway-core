#!/bin/bash
# GLM-5.2 降级问题 - 快速部署指南
# 执行时间: 5-10 分钟（紧急修复）

set -e

echo "=========================================="
echo "GLM-5.2 降级问题快速修复"
echo "=========================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 配置（请根据实际环境修改）
DB_HOST="${DB_HOST:-172.31.86.245}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-postgres}"
PROVIDER_NAME="${PROVIDER_NAME:-sp1}"
CREDENTIAL_LABEL="${CREDENTIAL_LABEL:-spi-3}"

echo -e "${BLUE}[步骤 1/5]${NC} 检查数据库连接..."
if PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -c "SELECT 1" > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} 数据库连接成功"
else
    echo -e "${RED}✗${NC} 数据库连接失败，请检查配置"
    echo "提示: export DB_PASSWORD=your_password"
    exit 1
fi

echo ""
echo -e "${BLUE}[步骤 2/5]${NC} 查找目标 credential..."
CREDENTIAL_ID=$(PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -t -A -c "
    SELECT c.id
    FROM credentials c
    JOIN providers p ON c.provider_id = p.id
    WHERE p.name = '${PROVIDER_NAME}'
      AND c.label LIKE '%${CREDENTIAL_LABEL}%'
    LIMIT 1
")

if [ -z "$CREDENTIAL_ID" ]; then
    echo -e "${RED}✗${NC} 未找到匹配的 credential"
    echo "提示: 检查 PROVIDER_NAME 和 CREDENTIAL_LABEL 配置"
    exit 1
fi

echo -e "${GREEN}✓${NC} 找到 credential_id: ${CREDENTIAL_ID}"

echo ""
echo -e "${BLUE}[步骤 3/5]${NC} 执行数据库修复..."

# 保护 glm-5.2 binding
PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -c "
    UPDATE credential_model_bindings cmb
    SET 
        admin_protected = TRUE,
        manual_priority = 100,
        updated_at = NOW()
    FROM provider_models pm
    WHERE cmb.provider_model_id = pm.id
      AND cmb.credential_id = ${CREDENTIAL_ID}
      AND pm.raw_model_name = 'glm-5.2';
" > /dev/null 2>&1

echo -e "${GREEN}✓${NC} 已设置 admin_protected 和 manual_priority"

# 恢复降级状态
RECOVERED=$(PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -t -A -c "
    WITH updated AS (
        UPDATE credential_model_bindings cmb
        SET 
            available = TRUE,
            unavailable_reason = NULL,
            unavailable_at = NULL,
            unavailable_recover_at = NULL,
            updated_at = NOW()
        FROM provider_models pm
        WHERE cmb.provider_model_id = pm.id
          AND cmb.credential_id = ${CREDENTIAL_ID}
          AND pm.raw_model_name = 'glm-5.2'
          AND cmb.available = FALSE
          AND cmb.unavailable_reason = 'continuous_failure'
        RETURNING cmb.id
    )
    SELECT COUNT(*) FROM updated;
")

if [ "$RECOVERED" -gt 0 ]; then
    echo -e "${GREEN}✓${NC} 恢复了 ${RECOVERED} 个降级的 binding"
else
    echo -e "${YELLOW}⚠${NC} 没有需要恢复的降级 binding（可能已经可用）"
fi

# 清理 probe 状态
PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -c "
    DELETE FROM node_probe_state nps
    USING credentials c, providers p
    WHERE nps.credential_id = c.id
      AND c.provider_id = p.id
      AND c.id = ${CREDENTIAL_ID}
      AND nps.raw_model_name = 'glm-5.2'
      AND nps.last_direct_ok = FALSE;
" > /dev/null 2>&1

echo -e "${GREEN}✓${NC} 已清理 probe 失败状态"

echo ""
echo -e "${BLUE}[步骤 4/5]${NC} 验证修复效果..."

# 检查是否可路由
IS_ROUTABLE=$(PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -t -A -c "
    SELECT is_routable
    FROM v_routable_credential_models
    WHERE raw_model_name = 'glm-5.2'
      AND credential_id = ${CREDENTIAL_ID}
    LIMIT 1
")

if [ "$IS_ROUTABLE" = "t" ]; then
    echo -e "${GREEN}✓${NC} GLM-5.2 已可路由"
else
    echo -e "${RED}✗${NC} GLM-5.2 仍不可路由，可能有其他阻塞原因"
    echo "执行以下 SQL 查看详情："
    echo "  SELECT * FROM v_routable_credential_models WHERE credential_id = ${CREDENTIAL_ID} AND raw_model_name = 'glm-5.2';"
fi

echo ""
echo -e "${BLUE}[步骤 5/5]${NC} 显示当前状态..."

PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -c "
    SELECT 
        c.id AS credential_id,
        c.label,
        cmb.admin_protected,
        cmb.manual_priority,
        cmb.available,
        cmb.unavailable_reason
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON cmb.provider_model_id = pm.id
    JOIN credentials c ON cmb.credential_id = c.id
    WHERE c.id = ${CREDENTIAL_ID}
      AND pm.raw_model_name = 'glm-5.2';
"

echo ""
echo "=========================================="
echo -e "${GREEN}紧急修复完成！${NC}"
echo "=========================================="
echo ""
echo "下一步:"
echo "  1. 观察 15-30 分钟，确认降级频率下降"
echo "  2. 查看日志: ssh root@${DB_HOST} \"docker-compose logs -f llm-gateway | grep glm-5.2\""
echo "  3. 完整修复请部署代码: bash scripts/fix-glm5.2-degradation.sh"
echo ""
echo "监控指标:"
echo "  - 降级频率: rate(credential_degradation_total{model=\"glm-5.2\"}[5m])"
echo "  - 成功率: rate(request_total{model=\"glm-5.2\", status=\"success\"}[5m])"
echo ""
