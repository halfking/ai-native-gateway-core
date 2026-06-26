#!/bin/bash
# ============================================================================
# llm-gateway-go 数据库初始化脚本
# ============================================================================

set -euo pipefail

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 默认配置
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 用法说明
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -h HOST     数据库主机 (default: localhost)"
    echo "  -p PORT     数据库端口 (default: 5432)"
    echo "  -u USER     数据库用户 (default: postgres)"
    echo "  -d NAME     数据库名 (default: llm_gateway)"
    echo "  --skip-seed 跳过初始化数据"
    echo "  --help      显示帮助"
    echo ""
    echo "Environment variables:"
    echo "  DB_HOST, DB_PORT, DB_USER, DB_NAME"
    exit 1
}

# 解析参数
SKIP_SEED=false
while [[ $# -gt 0 ]]; do
    case $1 in
        -h) DB_HOST="$2"; shift 2 ;;
        -p) DB_PORT="$2"; shift 2 ;;
        -u) DB_USER="$2"; shift 2 ;;
        -d) DB_NAME="$2"; shift 2 ;;
        --skip-seed) SKIP_SEED=true; shift ;;
        --help) usage ;;
        *) echo "Unknown option: $1"; usage ;;
    esac
done

# 打印配置
echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}LLM Gateway 数据库初始化${NC}"
echo -e "${YELLOW}========================================${NC}"
echo "主机: $DB_HOST:$DB_PORT"
echo "用户: $DB_USER"
echo "数据库: $DB_NAME"
echo ""

# 检查连接
echo -e "${YELLOW}[1/5] 检查数据库连接...${NC}"
if ! PGPASSWORD="${PGPASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1" > /dev/null 2>&1; then
    echo -e "${RED}错误: 无法连接到数据库${NC}"
    echo "请检查数据库是否运行，以及连接配置是否正确"
    exit 1
fi
echo -e "${GREEN}✓ 数据库连接正常${NC}"

# 检查扩展
echo -e "${YELLOW}[2/5] 检查扩展...${NC}"
EXTENSIONS=$(PGPASSWORD="${PGPASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT COUNT(*) FROM pg_extension WHERE extname IN ('citus', 'columnar');")
if [[ $(echo "$EXTENSIONS" | tr -d ' ') -lt 2 ]]; then
    echo -e "${YELLOW}警告: Citus 或 Columnar 扩展可能未安装${NC}"
    echo "请确保扩展已安装:"
    echo "  CREATE EXTENSION IF NOT EXISTS citus;"
    echo "  CREATE EXTENSION IF NOT EXISTS columnar;"
fi
echo -e "${GREEN}✓ 扩展检查完成${NC}"

# 执行表结构
echo -e "${YELLOW}[3/5] 创建表结构...${NC}"
SCHEMA_DIR="$SCRIPT_DIR/../00_schema"
for f in "$SCHEMA_DIR"/*.sql; do
    if [[ -f "$f" ]]; then
        echo "  执行: $(basename "$f")"
        PGPASSWORD="${PGPASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$f" > /dev/null 2>&1
    fi
done
echo -e "${GREEN}✓ 表结构创建完成${NC}"

# 执行函数
echo -e "${YELLOW}[4/5] 创建函数和触发器...${NC}"
FUNC_FILE="$SCRIPT_DIR/../01_functions/functions.sql"
if [[ -f "$FUNC_FILE" ]]; then
    PGPASSWORD="${PGPASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$FUNC_FILE" > /dev/null 2>&1
    echo -e "${GREEN}✓ 函数创建完成${NC}"
fi

# 执行初始化数据
echo -e "${YELLOW}[5/5] 初始化数据...${NC}"
if [[ "$SKIP_SEED" == "false" ]]; then
    SEED_DIR="$SCRIPT_DIR/../02_seed_data"
    for f in "$SEED_DIR"/*.sql; do
        if [[ -f "$f" ]]; then
            echo "  执行: $(basename "$f")"
            PGPASSWORD="${PGPASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$f" > /dev/null 2>&1
        fi
    done
    echo -e "${GREEN}✓ 初始化数据完成${NC}"
else
    echo -e "${YELLOW}跳过初始化数据 (--skip-seed)${NC}"
fi

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}数据库初始化完成!${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "后续步骤:"
echo "1. 设置管理员密码: UPDATE users SET password_hash='\$2a\$10\$...' WHERE username='admin';"
echo "2. 配置提供商凭据: INSERT INTO credentials (provider_id, name, api_key, ...) VALUES (...);"
echo "3. 配置完成!"