#!/bin/bash
# 测试 Sessions V2 Migration
# 
# 用途：在本地或测试环境验证 Migration 430
# 
# 使用方法：
#   ./test-migration-430.sh [up|down|verify]
#
# 环境变量：
#   DB_URL - PostgreSQL 连接字符串
#   或使用默认本地配置

set -e  # 遇到错误立即退出

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 默认数据库连接（可通过环境变量覆盖）
DB_URL=${DB_URL:-"postgres://kxuser:kxpass@127.0.0.1:5432/llm_gateway?sslmode=disable"}

echo -e "${BLUE}=== Sessions V2 Migration 测试 ===${NC}"
echo "数据库: ${DB_URL%%\?*}"  # 隐藏密码部分
echo ""

# 获取脚本目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

MIGRATION_UP="${PROJECT_ROOT}/sql/migrations/startup/430_sessions_v2_schema.sql"
MIGRATION_DOWN="${PROJECT_ROOT}/sql/migrations/startup/430_sessions_v2_schema.down.sql"

# 检查文件是否存在
if [ ! -f "$MIGRATION_UP" ]; then
    echo -e "${RED}错误: 找不到 migration 文件: $MIGRATION_UP${NC}"
    exit 1
fi

if [ ! -f "$MIGRATION_DOWN" ]; then
    echo -e "${RED}错误: 找不到 migration 文件: $MIGRATION_DOWN${NC}"
    exit 1
fi

# 测试数据库连接
test_connection() {
    echo -e "${YELLOW}测试数据库连接...${NC}"
    if psql "$DB_URL" -c "SELECT version();" > /dev/null 2>&1; then
        echo -e "${GREEN}✓ 数据库连接成功${NC}"
        return 0
    else
        echo -e "${RED}✗ 数据库连接失败${NC}"
        echo "请检查数据库是否运行，或设置正确的 DB_URL 环境变量"
        echo "示例: export DB_URL='postgres://user:pass@localhost:5432/dbname'"
        return 1
    fi
}

# 执行 migration up
run_migration_up() {
    echo -e "${YELLOW}执行 Migration UP (创建V2表)...${NC}"
    if psql "$DB_URL" -f "$MIGRATION_UP"; then
        echo -e "${GREEN}✓ Migration UP 成功${NC}"
        return 0
    else
        echo -e "${RED}✗ Migration UP 失败${NC}"
        return 1
    fi
}

# 执行 migration down
run_migration_down() {
    echo -e "${YELLOW}执行 Migration DOWN (删除V2表)...${NC}"
    if psql "$DB_URL" -f "$MIGRATION_DOWN"; then
        echo -e "${GREEN}✓ Migration DOWN 成功${NC}"
        return 0
    else
        echo -e "${RED}✗ Migration DOWN 失败${NC}"
        return 1
    fi
}

# 验证表结构
verify_tables() {
    echo -e "${YELLOW}验证表结构...${NC}"
    
    # 检查4张核心表
    tables=(
        "gateway.sessions"
        "gateway.session_turns"
        "gateway.session_bodies"
        "gateway.session_turn_logs"
    )
    
    all_exist=true
    for table in "${tables[@]}"; do
        if psql "$DB_URL" -c "SELECT 1 FROM $table LIMIT 0;" > /dev/null 2>&1; then
            echo -e "${GREEN}  ✓ $table 存在${NC}"
        else
            echo -e "${RED}  ✗ $table 不存在${NC}"
            all_exist=false
        fi
    done
    
    if [ "$all_exist" = true ]; then
        echo -e "${GREEN}✓ 所有表都已创建${NC}"
    else
        echo -e "${RED}✗ 部分表缺失${NC}"
        return 1
    fi
    
    echo ""
    
    # 验证分区
    echo -e "${YELLOW}验证分区...${NC}"
    partition_count=$(psql "$DB_URL" -t -c "
        SELECT COUNT(*) 
        FROM pg_tables 
        WHERE schemaname = 'gateway' 
        AND tablename LIKE 'sessions_%'
        AND tablename ~ '_[0-9]{4}_[0-9]{2}$';
    " | xargs)
    
    echo -e "  当前分区数: ${GREEN}${partition_count}${NC}"
    
    if [ "$partition_count" -ge 2 ]; then
        echo -e "${GREEN}  ✓ 分区已创建 (当前月+下月)${NC}"
    else
        echo -e "${YELLOW}  ⚠ 分区数量少于预期${NC}"
    fi
    
    echo ""
    
    # 验证RLS
    echo -e "${YELLOW}验证RLS策略...${NC}"
    rls_count=$(psql "$DB_URL" -t -c "
        SELECT COUNT(*) 
        FROM pg_tables 
        WHERE schemaname = 'gateway'
        AND tablename IN ('sessions', 'session_turns', 'session_bodies')
        AND rowsecurity = true;
    " | xargs)
    
    if [ "$rls_count" -eq 3 ]; then
        echo -e "${GREEN}  ✓ RLS已启用 (3/3)${NC}"
    else
        echo -e "${RED}  ✗ RLS未完全启用 ($rls_count/3)${NC}"
        return 1
    fi
    
    echo ""
    
    # 验证函数
    echo -e "${YELLOW}验证管理函数...${NC}"
    functions=(
        "ensure_sessions_v2_partitions"
        "cleanup_expired_session_turn_logs"
    )
    
    for func in "${functions[@]}"; do
        if psql "$DB_URL" -c "SELECT 1 FROM pg_proc WHERE proname = '$func';" | grep -q "1 row"; then
            echo -e "${GREEN}  ✓ $func() 存在${NC}"
        else
            echo -e "${RED}  ✗ $func() 不存在${NC}"
            return 1
        fi
    done
    
    echo -e "${GREEN}✓ 所有验证通过${NC}"
    return 0
}

# 显示表结构
show_schema() {
    echo -e "${YELLOW}显示表结构...${NC}"
    echo ""
    
    echo -e "${BLUE}=== gateway.session_turns ===${NC}"
    psql "$DB_URL" -c "\d gateway.session_turns"
    
    echo ""
    echo -e "${BLUE}=== gateway.sessions ===${NC}"
    psql "$DB_URL" -c "\d gateway.sessions"
    
    echo ""
    echo -e "${BLUE}=== 分区列表 ===${NC}"
    psql "$DB_URL" -c "
        SELECT tablename 
        FROM pg_tables 
        WHERE schemaname = 'gateway' 
        AND tablename LIKE 'session%'
        ORDER BY tablename;
    "
}

# 插入测试数据
insert_test_data() {
    echo -e "${YELLOW}插入测试数据...${NC}"
    
    psql "$DB_URL" << 'EOF'
-- 插入测试会话
INSERT INTO gateway.sessions (
    session_id, tenant_id, 
    total_turns, total_tokens, total_cost_usd,
    primary_request_id, partition_date
) VALUES (
    'test_session_001', 'default',
    2, 1000, 0.01,
    'test_req_001', CURRENT_DATE
) ON CONFLICT (session_id, partition_date) DO NOTHING;

-- 插入测试轮次
INSERT INTO gateway.session_turns (
    session_id, turn_no, tenant_id, request_id,
    model, provider, prompt_tokens, completion_tokens, cost_usd,
    source_kind, quality, partition_date, ts
) VALUES 
(
    'test_session_001', 1, 'default', 'test_req_001',
    'gpt-4', 'openai', 500, 300, 0.005,
    'live', 'verified', CURRENT_DATE, NOW()
),
(
    'test_session_001', 2, 'default', 'test_req_002',
    'gpt-4', 'openai', 150, 50, 0.002,
    'live', 'verified', CURRENT_DATE, NOW()
)
ON CONFLICT (request_id, partition_date) DO NOTHING;

-- 验证插入
SELECT 
    session_id, 
    total_turns, 
    total_tokens,
    total_cost_usd
FROM gateway.sessions 
WHERE session_id = 'test_session_001';

SELECT 
    turn_no,
    request_id,
    model,
    prompt_tokens,
    completion_tokens
FROM gateway.session_turns 
WHERE session_id = 'test_session_001'
ORDER BY turn_no;
EOF
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}✓ 测试数据插入成功${NC}"
    else
        echo -e "${RED}✗ 测试数据插入失败${NC}"
        return 1
    fi
}

# 清理测试数据
cleanup_test_data() {
    echo -e "${YELLOW}清理测试数据...${NC}"
    
    psql "$DB_URL" << 'EOF'
DELETE FROM gateway.session_turns WHERE session_id = 'test_session_001';
DELETE FROM gateway.sessions WHERE session_id = 'test_session_001';
EOF
    
    echo -e "${GREEN}✓ 测试数据已清理${NC}"
}

# 主流程
main() {
    local action="${1:-full}"
    
    case "$action" in
        up)
            test_connection || exit 1
            run_migration_up || exit 1
            verify_tables || exit 1
            ;;
        down)
            test_connection || exit 1
            run_migration_down || exit 1
            ;;
        verify)
            test_connection || exit 1
            verify_tables || exit 1
            ;;
        schema)
            test_connection || exit 1
            show_schema
            ;;
        test)
            test_connection || exit 1
            insert_test_data || exit 1
            cleanup_test_data || exit 1
            ;;
        full)
            echo -e "${BLUE}=== 完整测试流程 ===${NC}"
            echo ""
            
            test_connection || exit 1
            echo ""
            
            echo -e "${BLUE}步骤 1/5: 执行 Migration UP${NC}"
            run_migration_up || exit 1
            echo ""
            
            echo -e "${BLUE}步骤 2/5: 验证表结构${NC}"
            verify_tables || exit 1
            echo ""
            
            echo -e "${BLUE}步骤 3/5: 插入测试数据${NC}"
            insert_test_data || exit 1
            echo ""
            
            echo -e "${BLUE}步骤 4/5: 清理测试数据${NC}"
            cleanup_test_data || exit 1
            echo ""
            
            echo -e "${BLUE}步骤 5/5: 执行 Migration DOWN${NC}"
            run_migration_down || exit 1
            echo ""
            
            echo -e "${GREEN}=== ✓ 所有测试通过 ===${NC}"
            ;;
        *)
            echo "用法: $0 [up|down|verify|schema|test|full]"
            echo ""
            echo "命令："
            echo "  up      - 执行 migration (创建表)"
            echo "  down    - 回滚 migration (删除表)"
            echo "  verify  - 验证表结构"
            echo "  schema  - 显示表结构"
            echo "  test    - 插入并清理测试数据"
            echo "  full    - 完整测试流程 (默认)"
            echo ""
            echo "环境变量："
            echo "  DB_URL  - 数据库连接字符串"
            echo "            默认: postgres://kxuser:kxpass@127.0.0.1:5432/llm_gateway"
            exit 1
            ;;
    esac
}

# 运行主流程
main "$@"
