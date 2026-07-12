#!/bin/bash
# 验证分区表查询修复
# 验证 hot 表、视图、父表的数据一致性
# 作者：llm-gateway-ops
# 日期：2026-07-06

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 数据库连接信息（通过 SSH 隧道连接 184 服务器）
DB_HOST="10.43.237.99"
DB_PORT="5432"
DB_USER="llm_gateway"
DB_NAME="llm_gateway"
DB_PASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"
SSH_HOST="root@47.97.111.154"  # 154 替代 184
SSH_PORT="25022"

# 日志函数
function log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
function log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
function log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
function log_section() { echo -e "\n${BLUE}========================================${NC}"; echo -e "${BLUE}$1${NC}"; echo -e "${BLUE}========================================${NC}"; }

# 通过 SSH 执行 PostgreSQL 命令
function psql_exec() {
    ssh -p $SSH_PORT $SSH_HOST "PGPASSWORD='$DB_PASSWORD' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"$1\"" 2>/dev/null
}

# 统计
TOTAL_CHECKS=0
PASSED_CHECKS=0
FAILED_CHECKS=0

# ============================================================
# 测试 1: 验证 hot 表和视图数据一致性
# ============================================================
function test_hot_view_consistency() {
    log_section "测试 1: hot 表与视图数据一致性"
    
    for table in "request_logs" "usage_ledger" "credit_ledger" "tool_usage_stats"; do
        ((TOTAL_CHECKS++))
        
        local hot_table="${table}_hot"
        local view_table="${table}_with_current_month"
        
        log_info "检查 $table ..."
        
        # 获取 hot 表行数
        local hot_count=$(psql_exec "SELECT count(*) FROM $hot_table;" | xargs)
        
        # 获取视图行数
        local view_count=$(psql_exec "SELECT count(*) FROM $view_table;" | xargs)
        
        # 获取父表行数
        local parent_count=$(psql_exec "SELECT count(*) FROM $table;" | xargs)
        
        log_info "  hot表: $hot_count 行"
        log_info "  视图: $view_count 行"
        log_info "  父表: $parent_count 行"
        
        # 验证：视图行数 >= hot表行数（视图 = hot + 历史分区）
        if [ "$view_count" -ge "$hot_count" ]; then
            log_info "  ✓ 视图包含 hot 表数据"
            ((PASSED_CHECKS++))
        else
            log_error "  ✗ 视图行数 ($view_count) < hot表行数 ($hot_count)"
            ((FAILED_CHECKS++))
        fi
        
        # 如果 hot 表有数据，验证最新记录在视图中可见
        if [ "$hot_count" -gt 0 ]; then
            ((TOTAL_CHECKS++))
            
            local hot_latest=$(psql_exec "SELECT ts FROM $hot_table ORDER BY ts DESC LIMIT 1;" | xargs)
            local view_latest=$(psql_exec "SELECT ts FROM $view_table ORDER BY ts DESC LIMIT 1;" | xargs)
            
            if [ "$hot_latest" == "$view_latest" ]; then
                log_info "  ✓ 视图包含 hot 表最新记录 ($hot_latest)"
                ((PASSED_CHECKS++))
            else
                log_error "  ✗ 视图最新记录 ($view_latest) != hot表最新记录 ($hot_latest)"
                ((FAILED_CHECKS++))
            fi
        fi
        
        echo ""
    done
}

# ============================================================
# 测试 2: 验证索引存在
# ============================================================
function test_indexes() {
    log_section "测试 2: 索引完整性检查"
    
    for table in "request_logs" "usage_ledger" "credit_ledger" "tool_usage_stats"; do
        local hot_table="${table}_hot"
        
        ((TOTAL_CHECKS++))
        
        log_info "检查 $hot_table 索引..."
        
        # 获取索引数量
        local index_count=$(psql_exec "SELECT count(*) FROM pg_indexes WHERE tablename = '$hot_table';" | xargs)
        
        if [ "$index_count" -gt 0 ]; then
            log_info "  ✓ $hot_table 有 $index_count 个索引"
            ((PASSED_CHECKS++))
            
            # 列出索引
            psql_exec "SELECT indexname FROM pg_indexes WHERE tablename = '$hot_table' ORDER BY indexname;" | while read idx; do
                [ -z "$idx" ] && continue
                log_info "    - $idx"
            done
        else
            log_error "  ✗ $hot_table 没有索引"
            ((FAILED_CHECKS++))
        fi
        
        echo ""
    done
}

# ============================================================
# 测试 3: 验证代码中没有直接查询父表
# ============================================================
function test_code_queries() {
    log_section "测试 3: 代码查询审计"
    
    ((TOTAL_CHECKS++))
    
    log_info "检查 admin/logs.go 是否使用视图..."
    
    local bad_queries=$(grep -n "FROM request_logs " admin/logs.go | grep -v "_hot" | grep -v "_with_current_month" | wc -l | xargs)
    
    if [ "$bad_queries" -eq 0 ]; then
        log_info "  ✓ admin/logs.go 所有查询都使用视图或 hot 表"
        ((PASSED_CHECKS++))
    else
        log_error "  ✗ admin/logs.go 有 $bad_queries 处直接查询父表"
        ((FAILED_CHECKS++))
    fi
    
    echo ""
}

# ============================================================
# 测试 4: 插入测试数据验证
# ============================================================
function test_insert_query() {
    log_section "测试 4: 插入查询验证"
    
    ((TOTAL_CHECKS++))
    
    log_info "测试插入到 hot 表并从视图查询..."
    
    local test_request_id="test-$(date +%s)-$$"
    
    # 插入测试数据到 hot 表
    ssh -p $SSH_PORT $SSH_HOST "PGPASSWORD='$DB_PASSWORD' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c \"
        INSERT INTO request_logs_hot (
            ts, request_id, success, client_model, latency_ms, prompt_tokens, completion_tokens, total_tokens
        ) VALUES (
            NOW(), '$test_request_id', true, 'test-model', 100, 10, 20, 30
        );
    \"" > /dev/null 2>&1
    
    sleep 1
    
    # 从视图查询
    local found=$(psql_exec "SELECT count(*) FROM request_logs_with_current_month WHERE request_id = '$test_request_id';" | xargs)
    
    if [ "$found" -eq 1 ]; then
        log_info "  ✓ 插入到 hot 表的数据可从视图查询到"
        ((PASSED_CHECKS++))
    else
        log_error "  ✗ 插入到 hot 表的数据在视图中找不到"
        ((FAILED_CHECKS++))
    fi
    
    # 清理测试数据
    ssh -p $SSH_PORT $SSH_HOST "PGPASSWORD='$DB_PASSWORD' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c \"
        DELETE FROM request_logs_hot WHERE request_id = '$test_request_id';
    \"" > /dev/null 2>&1
    
    echo ""
}

# ============================================================
# 主函数
# ============================================================
function main() {
    log_section "开始验证分区表查询修复"
    
    echo "数据库: $DB_HOST:$DB_PORT/$DB_NAME (通过 SSH $SSH_HOST)"
    echo ""
    
    # 执行测试
    test_hot_view_consistency
    test_indexes
    test_code_queries
    test_insert_query
    
    # 输出总结
    log_section "验证总结"
    echo "总检查项: $TOTAL_CHECKS"
    echo -e "${GREEN}通过: $PASSED_CHECKS${NC}"
    echo -e "${RED}失败: $FAILED_CHECKS${NC}"
    echo ""
    
    if [ $FAILED_CHECKS -eq 0 ]; then
        log_info "✅ 所有检查通过！"
        exit 0
    else
        log_error "❌ 有 $FAILED_CHECKS 项检查失败"
        exit 1
    fi
}

# 运行
main
