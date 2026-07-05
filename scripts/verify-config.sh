#!/usr/bin/env bash
# verify-config.sh — 启动前环境变量完整性校验
#
# 用法:
#   ./scripts/verify-config.sh             # 普通模式，缺失项仅警告
#   ./scripts/verify-config.sh --strict    # 严格模式，缺失项 exit 1
#   ./scripts/verify-config.sh --prod      # 生产模式，所有生产必填项校验
#
# 返回码:
#   0 — 所有检查通过
#   1 — 有缺失项（strict/prod 模式）

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

MODE="${1:-normal}"
FAILED=0
WARNINGS=()

# ------ 颜色 ------
RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
NC='\033[0m'

check_var() {
    local var="$1"
    local label="${2:-$var}"
    local required="${3:-no}"

    if [ -z "${!var:-}" ]; then
        if [ "$required" = "yes" ] || [ "$MODE" = "--strict" ] || [ "$MODE" = "--prod" ]; then
            echo -e "  ${RED}❌ MISSING${NC}  $label" >&2
            FAILED=1
        else
            echo -e "  ${YELLOW}⚠️  MISSING${NC}  $label (optional)" >&2
            WARNINGS+=("$label")
        fi
    else
        local val="${!var}"
        echo -e "  ${GREEN}✅ OK${NC}      $label = ${val:0:20}..." >&2
    fi
}

# ====== 入口 ======
echo "" >&2
echo "═══════════════════════════════════════════" >&2
echo "  LLM Gateway 配置验证" >&2
echo "  模式: $MODE" >&2
echo "═══════════════════════════════════════════" >&2
echo "" >&2

# ------ Step 1: 加载环境变量（如有 .env.local）------
if [ -f "$PROJECT_DIR/.env.local" ]; then
    echo "[1/3] 加载 .env.local..." >&2
    set -a
    source "$PROJECT_DIR/.env.local"
    set +a
    echo "" >&2
fi

# ------ Step 2: 核心必填项 ------
echo "[2/3] 核心配置检查..." >&2

# 数据库
check_var "LLM_GATEWAY_DATABASE_URL" "数据库连接" "yes"
check_var "LLM_GATEWAY_REDIS_ADDR" "Redis 地址"
check_var "LLM_GATEWAY_REDIS_PASSWORD" "Redis 密码"

# 认证
if [ "$MODE" = "--prod" ]; then
    check_var "LLM_GATEWAY_API_KEY" "网关 API Key" "yes"
    check_var "LLM_GATEWAY_ADMIN_API_KEY" "管理 API Key" "yes"
    check_var "LLM_GATEWAY_SECRET_KEY" "JWT 密钥" "yes"
else
    check_var "LLM_GATEWAY_API_KEY" "网关 API Key"
    check_var "LLM_GATEWAY_ADMIN_API_KEY" "管理 API Key"
    check_var "LLM_GATEWAY_SECRET_KEY" "JWT 密钥"
fi

# 加密密钥
check_var "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY" "凭据加密密钥"
check_var "LLM_GATEWAY_IDENTITY_SALT" "身份盐值"

# 服务
check_var "LLM_GATEWAY_LISTEN" "监听地址 (LLM_GATEWAY_LISTEN)"
check_var "LLM_GATEWAY_ENV" "运行环境 (LLM_GATEWAY_ENV)"

# 部署相关
check_var "LLM_GATEWAY_184_HOST" "184 服务器地址"
check_var "LLM_GATEWAY_71_HOST" "71 服务器地址"

echo "" >&2

# ------ Step 3: 外部依赖可用性 ------
echo "[3/3] 外部依赖检查..." >&2

# 数据库连接检查（可选）
if [ -n "${LLM_GATEWAY_DATABASE_URL:-}" ]; then
    if command -v psql &>/dev/null; then
        echo -n "  PostgreSQL 连接测试..." >&2
        if psql "${LLM_GATEWAY_DATABASE_URL}" -c "SELECT 1" &>/dev/null; then
            echo -e " ${GREEN}✅ OK${NC}" >&2
        else
            echo -e " ${YELLOW}⚠️  FAILED${NC} (无法连接数据库，跳过)" >&2
        fi
    else
        echo -e "  PostgreSQL ${YELLOW}⚠️  psql 未安装，跳过连接测试${NC}" >&2
    fi
fi

# Redis 连接检查（可选）
if [ -n "${LLM_GATEWAY_REDIS_ADDR:-}" ]; then
    if command -v redis-cli &>/dev/null; then
        echo -n "  Redis 连接测试..." >&2
        if redis-cli -u "${LLM_GATEWAY_REDIS_ADDR}" PING &>/dev/null; then
            echo -e " ${GREEN}✅ OK${NC}" >&2
        else
            echo -e " ${YELLOW}⚠️  FAILED${NC} (无法连接 Redis，跳过)" >&2
        fi
    else
        echo -e "  Redis ${YELLOW}⚠️  redis-cli 未安装，跳过连接测试${NC}" >&2
    fi
fi

echo "" >&2

# ====== 结果汇总 ======
echo "═══════════════════════════════════════════" >&2
if [ "$FAILED" = "1" ]; then
    echo -e "  ${RED}❌ 验证失败: 存在缺失的必填配置项${NC}" >&2
    echo "  请检查环境变量设置。详情见 docs/CONFIGURATION_GUIDE.md" >&2
    echo "═══════════════════════════════════════════" >&2
    echo "" >&2
    exit 1
elif [ ${#WARNINGS[@]} -gt 0 ]; then
    echo -e "  ${YELLOW}⚠️  验证通过（${#WARNINGS[@]} 个可选配置缺失）${NC}" >&2
    echo "═══════════════════════════════════════════" >&2
    echo "" >&2
    exit 0
else
    echo -e "  ${GREEN}✅ 验证全部通过${NC}" >&2
    echo "═══════════════════════════════════════════" >&2
    echo "" >&2
    exit 0
fi
