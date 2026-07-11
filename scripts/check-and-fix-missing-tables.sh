#!/bin/bash
# check-and-fix-missing-tables.sh - 检查并修复缺失的数据库表
#
# 用法:
#   ./scripts/check-and-fix-missing-tables.sh --check [环境]
#   ./scripts/check-and-fix-missing-tables.sh --fix [环境]
#
# 环境:
#   local      - 本地开发数据库
#   kaixuan-1  - kaixuan-1 k3s数据库
#   154        - 154生产环境数据库
#   all        - 所有环境

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 配置
MODE="${1:---check}"
ENV="${2:-all}"

# 数据库连接配置。敏感连接串通过环境变量提供：
# LLM_GATEWAY_DATABASE_URL_LOCAL / _KAIXUAN_1 / _154。
declare -A DB_CONFIGS=(
    ["local"]="${LLM_GATEWAY_DATABASE_URL_LOCAL:-}"
    ["kaixuan-1"]="${LLM_GATEWAY_DATABASE_URL_KAIXUAN_1:-}"
    ["154"]="${LLM_GATEWAY_DATABASE_URL_154:-}"
)

# 需要检查的表和视图
REQUIRED_TABLES=(
    "session_dim"
    "session_summaries"
    "session_owners"
    "session_tags"
    "session_clusters"
    "session_cluster_members"
    "session_request_summaries"
    "session_embeddings"
    "session_optimization_suggestions"
    "session_intent_evolution"
)

REQUIRED_VIEWS=(
    "v_session_analytics"
    "session_stats_today"
)

# 迁移文件列表（按顺序执行）
MIGRATION_FILES=(
    "sql/migrations/startup/350_session_analytics_fix.sql"
    "sql/migrations/startup/351_session_analytics_tables.sql"
    "sql/migrations/startup/355_session_analytics_indexes.sql"
    "sql/migrations/startup/356_session_health_columns.sql"
    "sql/migrations/startup/357_session_analytics_aggregation_views.sql"
    "sql/migrations/startup/358_session_ownership.sql"
    "sql/migrations/startup/359_session_intent_evolution.sql"
)

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}  数据库表完整性检查和修复工具${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# 检查单个环境
check_environment() {
    local env=$1
    local db_url=${DB_CONFIGS[$env]}
    
    echo -e "${YELLOW}检查环境: $env${NC}"
    echo ""

    if [ -z "$db_url" ]; then
        echo -e "${RED}✗ 未配置 $env 的数据库连接串${NC}"
        echo "  请设置对应的 LLM_GATEWAY_DATABASE_URL_* 环境变量"
        return 2
    fi
    
    if [ "$env" = "local" ]; then
        # 检查本地数据库是否运行
        if ! pg_isready -h localhost -p 5432 &>/dev/null; then
            echo -e "${RED}✗ 本地PostgreSQL未运行${NC}"
            echo -e "  请先启动数据库: brew services start postgresql"
            return 1
        fi
    fi
    
    # 检查表
    echo "表检查结果:"
    local missing_tables=()
    for table in "${REQUIRED_TABLES[@]}"; do
        local exists=$(psql "$db_url" -t -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_name='$table'" 2>/dev/null || echo "0")
        exists=$(echo "$exists" | xargs)
        
        if [ "$exists" = "1" ]; then
            echo -e "  ✅ $table"
        else
            echo -e "  ${RED}❌ $table (缺失)${NC}"
            missing_tables+=("$table")
        fi
    done
    
    echo ""
    echo "视图检查结果:"
    local missing_views=()
    for view in "${REQUIRED_VIEWS[@]}"; do
        local exists=$(psql "$db_url" -t -c "SELECT COUNT(*) FROM information_schema.views WHERE table_name='$view'" 2>/dev/null || echo "0")
        exists=$(echo "$exists" | xargs)
        
        if [ "$exists" = "1" ]; then
            echo -e "  ✅ $view"
        else
            echo -e "  ${RED}❌ $view (缺失)${NC}"
            missing_views+=("$view")
        fi
    done
    
    echo ""
    
    if [ ${#missing_tables[@]} -eq 0 ] && [ ${#missing_views[@]} -eq 0 ]; then
        echo -e "${GREEN}✅ 所有表和视图完整${NC}"
        return 0
    else
        echo -e "${YELLOW}⚠️  发现缺失项目:${NC}"
        echo -e "   缺失表: ${#missing_tables[@]} 个"
        echo -e "   缺失视图: ${#missing_views[@]} 个"
        
        if [ "$MODE" = "--fix" ]; then
            echo ""
            fix_environment "$env" "$db_url"
        fi
        return 1
    fi
}

# 修复单个环境
fix_environment() {
    local env=$1
    local db_url=$2
    
    echo -e "${YELLOW}开始修复 $env 环境...${NC}"
    echo ""
    
    for migration_file in "${MIGRATION_FILES[@]}"; do
        local file_path="$REPO_DIR/$migration_file"
        
        if [ ! -f "$file_path" ]; then
            echo -e "${YELLOW}⚠️  跳过不存在的文件: $migration_file${NC}"
            continue
        fi
        
        echo -e "执行迁移: $(basename $migration_file)..."
        
        if [ "$env" = "154" ]; then
            # 154需要通过SSH上传并执行
            local remote_file="/tmp/$(basename $migration_file)"
            if [ -z "${DEPLOY_SSH_PASSWORD:-}" ]; then
                echo -e "  ${RED}✗ 154 修复需要 DEPLOY_SSH_PASSWORD${NC}"
                return 1
            fi
            sshpass -p "$DEPLOY_SSH_PASSWORD" scp -P 25022 "$file_path" "root@47.97.111.154:$remote_file"
            
            # 尝试通过应用程序执行（如果psql不可用）
            echo "  上传成功，但由于psql版本问题，请手动执行:"
            echo "  ssh -p 25022 root@47.97.111.154"
            echo "  # 在252数据库服务器上执行迁移"
        else
            # 本地或可直接连接的环境
            if psql "$db_url" -f "$file_path" &>/dev/null; then
                echo -e "  ${GREEN}✓ 执行成功${NC}"
            else
                echo -e "  ${RED}✗ 执行失败${NC}"
                psql "$db_url" -f "$file_path" 2>&1 | tail -5
            fi
        fi
        echo ""
    done
    
    echo -e "${GREEN}修复完成，请重新检查${NC}"
}

# 生成修复报告
generate_report() {
    local env=$1
    
    echo ""
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}  修复建议 - $env${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    
    case $env in
        local)
            echo "本地环境修复步骤:"
            echo "  1. 确保PostgreSQL运行: brew services start postgresql"
            echo "  2. 执行修复: ./scripts/check-and-fix-missing-tables.sh --fix local"
            ;;
        kaixuan-1)
            echo "kaixuan-1 K3s环境修复步骤:"
            echo "  1. 获取数据库连接信息"
            echo "  2. 设置 LLM_GATEWAY_DATABASE_URL_KAIXUAN_1"
            echo "  3. 执行修复: ./scripts/check-and-fix-missing-tables.sh --fix kaixuan-1"
            ;;
        154)
            echo "154生产环境修复步骤:"
            echo "  方案1（推荐）: 在252数据库服务器上执行"
            echo "    ssh <252-server>"
            echo "    psql -U llm_gateway -d llm_gateway -f <migration-file>"
            echo ""
            echo "  方案2: 通过堡垒机/跳板机"
            echo "    使用已有管理通道连接数据库执行迁移"
            echo ""
            echo "  方案3: 升级154服务器PostgreSQL客户端"
            echo "    修复YUM源后执行: yum install -y postgresql13"
            ;;
    esac
    echo ""
}

# 主函数
main() {
    if [ "$MODE" != "--check" ] && [ "$MODE" != "--fix" ]; then
        echo -e "${RED}错误: 无效的模式 '$MODE'${NC}"
        echo "用法: $0 [--check|--fix] [local|kaixuan-1|154|all]"
        exit 1
    fi
    
    if [ "$ENV" = "all" ]; then
        for env in "${!DB_CONFIGS[@]}"; do
            check_environment "$env" || true
            echo ""
            echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
            echo ""
        done
    else
        if [ -z "${DB_CONFIGS[$ENV]:-}" ]; then
            echo -e "${RED}错误: 未知环境 '$ENV'${NC}"
            echo "可用环境: ${!DB_CONFIGS[@]}"
            exit 1
        fi
        
        check_environment "$ENV" || generate_report "$ENV"
    fi
}

main "$@"
