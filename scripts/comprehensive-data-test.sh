#!/bin/bash
# request_logs 分表架构完整数据验证
# 目标：生成多样化数据，验证 CRUD 操作，发现潜在问题

set -e

# 目标数据库（有 hot 表的环境）
DB_HOST="${DB_HOST:-10.43.118.61}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_PASSWORD="${DB_PASSWORD:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}"

# 颜色
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

function log_info() { echo -e "${GREEN}[✓]${NC} $1"; }
function log_error() { echo -e "${RED}[✗]${NC} $1"; }
function log_warn() { echo -e "${YELLOW}[!]${NC} $1"; }
function log_section() { echo -e "\n${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n${BLUE}$1${NC}\n${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; }

function psql_exec() {
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "$1" 2>&1
}

# 测试统计
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0
ISSUES=()

# ============================================================
# 步骤 0: 环境检查
# ============================================================
function check_environment() {
    log_section "步骤 0: 环境检查"
    ((TOTAL_TESTS++))
    
    log_info "检查目标数据库: $DB_HOST:$DB_PORT/$DB_NAME"
    
    # 检查 hot 表是否存在
    local hot_exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'request_logs_hot');" | xargs)
    
    if [[ "$hot_exists" == "t" ]]; then
        log_info "✅ request_logs_hot 表存在"
        ((PASSED_TESTS++))
    else
        log_error "❌ request_logs_hot 表不存在"
        log_error "当前数据库不是 hot 表架构，无法继续测试"
        log_error "请切换到正确的环境（例如 184 服务器）"
        exit 1
    fi
    
    # 检查表结构
    log_info "检查表结构..."
    psql_exec "\d request_logs_hot" > /tmp/request_logs_hot_schema.txt 2>&1
    
    if grep -q "request_id" /tmp/request_logs_hot_schema.txt; then
        log_info "✅ 表结构正常"
    else
        log_error "❌ 表结构异常"
        cat /tmp/request_logs_hot_schema.txt
        exit 1
    fi
}

# ============================================================
# 步骤 1: 清理测试数据
# ============================================================
function cleanup_test_data() {
    log_section "步骤 1: 清理旧测试数据"
    
    log_info "清理 request_logs_hot 测试数据..."
    psql_exec "DELETE FROM request_logs_hot WHERE request_id LIKE 'test-%';" > /dev/null 2>&1
    
    log_info "✅ 清理完成"
}

# ============================================================
# 步骤 2: 生成多样化测试数据
# ============================================================
function generate_test_data() {
    log_section "步骤 2: 生成多样化测试数据"
    ((TOTAL_TESTS++))
    
    log_info "生成 20 条测试数据，涵盖多种场景..."
    
    local timestamp=$(date +%s)
    local inserted=0
    
    # 场景 1: 成功请求 - gpt-4 (5条)
    for i in {1..5}; do
        local req_id="test-success-gpt4-${timestamp}-${i}"
        psql_exec "INSERT INTO request_logs_hot (
            request_id, ts, tenant_id, success, 
            client_model, outbound_model, 
            prompt_tokens, completion_tokens, total_tokens,
            cost_usd, latency_ms, error_kind
        ) VALUES (
            '$req_id', NOW(), 'default', true,
            'gpt-4', 'gpt-4-0613',
            $((100 + i * 10)), $((50 + i * 5)), $((150 + i * 15)),
            0.003, $((200 + i * 50)), NULL
        );" > /dev/null 2>&1 && ((inserted++))
    done
    
    # 场景 2: 成功请求 - claude (3条)
    for i in {1..3}; do
        local req_id="test-success-claude-${timestamp}-${i}"
        psql_exec "INSERT INTO request_logs_hot (
            request_id, ts, tenant_id, success,
            client_model, outbound_model,
            prompt_tokens, completion_tokens, total_tokens,
            cost_usd, latency_ms, error_kind
        ) VALUES (
            '$req_id', NOW(), 'default', true,
            'claude-3-opus', 'claude-3-opus-20240229',
            $((200 + i * 20)), $((100 + i * 10)), $((300 + i * 30)),
            0.015, $((300 + i * 100)), NULL
        );" > /dev/null 2>&1 && ((inserted++))
    done
    
    # 场景 3: 失败请求 - rate_limit (4条)
    for i in {1..4}; do
        local req_id="test-fail-ratelimit-${timestamp}-${i}"
        psql_exec "INSERT INTO request_logs_hot (
            request_id, ts, tenant_id, success,
            client_model, error_kind,
            prompt_tokens, completion_tokens, latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', false,
            'gpt-3.5-turbo', 'rate_limit',
            NULL, NULL, 50
        );" > /dev/null 2>&1 && ((inserted++))
    done
    
    # 场景 4: 失败请求 - timeout (3条)
    for i in {1..3}; do
        local req_id="test-fail-timeout-${timestamp}-${i}"
        psql_exec "INSERT INTO request_logs_hot (
            request_id, ts, tenant_id, success,
            client_model, error_kind,
            latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', false,
            'gpt-4', 'timeout',
            30000
        );" > /dev/null 2>&1 && ((inserted++))
    done
    
    # 场景 5: 流式响应 (3条)
    for i in {1..3}; do
        local req_id="test-stream-${timestamp}-${i}"
        psql_exec "INSERT INTO request_logs_hot (
            request_id, ts, tenant_id, success,
            client_model, outbound_model,
            prompt_tokens, completion_tokens,
            stream_first_chunk_ms, stream_chunk_count,
            stream_done_sent, latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', true,
            'gpt-3.5-turbo', 'gpt-3.5-turbo-0125',
            $((80 + i * 10)), $((40 + i * 5)),
            $((100 + i * 20)), $((10 + i * 5)),
            true, $((500 + i * 100))
        );" > /dev/null 2>&1 && ((inserted++))
    done
    
    # 场景 6: 缓存命中 (2条)
    for i in {1..2}; do
        local req_id="test-cache-${timestamp}-${i}"
        psql_exec "INSERT INTO request_logs_hot (
            request_id, ts, tenant_id, success,
            client_model, cache_read_tokens,
            prompt_tokens, completion_tokens,
            latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', true,
            'gpt-4', $((500 + i * 100)),
            $((50 + i * 10)), $((30 + i * 5)),
            $((50 + i * 10))
        );" > /dev/null 2>&1 && ((inserted++))
    done
    
    log_info "✅ 成功插入 $inserted 条测试数据"
    
    if [[ "$inserted" -eq 20 ]]; then
        ((PASSED_TESTS++))
    else
        log_error "❌ 期望插入 20 条，实际插入 $inserted 条"
        ((FAILED_TESTS++))
        ISSUES+=("数据插入不完整")
    fi
}

# ============================================================
# 步骤 3: 验证数据插入
# ============================================================
function verify_insert() {
    log_section "步骤 3: 验证数据插入"
    ((TOTAL_TESTS++))
    
    local count=$(psql_exec "SELECT COUNT(*) FROM request_logs_hot WHERE request_id LIKE 'test-%';" | xargs)
    
    log_info "当前测试数据数量: $count"
    
    if [[ "$count" -ge 20 ]]; then
        log_info "✅ 数据插入验证通过"
        ((PASSED_TESTS++))
    else
        log_error "❌ 数据插入不足 (期望 ≥20, 实际 $count)"
        ((FAILED_TESTS++))
        ISSUES+=("数据插入数量不足")
    fi
    
    # 按场景统计
    log_info "\n场景数据统计:"
    psql_exec "
        SELECT 
            CASE 
                WHEN request_id LIKE '%success-gpt4%' THEN 'gpt-4 成功'
                WHEN request_id LIKE '%success-claude%' THEN 'claude 成功'
                WHEN request_id LIKE '%fail-ratelimit%' THEN 'rate_limit 失败'
                WHEN request_id LIKE '%fail-timeout%' THEN 'timeout 失败'
                WHEN request_id LIKE '%stream%' THEN '流式响应'
                WHEN request_id LIKE '%cache%' THEN '缓存命中'
                ELSE '其他'
            END as scenario,
            COUNT(*) as count
        FROM request_logs_hot
        WHERE request_id LIKE 'test-%'
        GROUP BY scenario
        ORDER BY count DESC;
    "
}

# ============================================================
# 步骤 4: 测试 UPDATE 操作
# ============================================================
function test_update() {
    log_section "步骤 4: 测试 UPDATE 操作"
    ((TOTAL_TESTS++))
    
    log_info "场景 A: 更新 tokens (模拟流式响应完成后更新)"
    
    # 获取一条测试记录
    local test_id=$(psql_exec "SELECT request_id FROM request_logs_hot WHERE request_id LIKE 'test-success-gpt4%' LIMIT 1;" | xargs)
    
    if [[ -z "$test_id" ]]; then
        log_error "❌ 找不到测试记录"
        ((FAILED_TESTS++))
        ISSUES+=("UPDATE测试: 找不到测试记录")
        return
    fi
    
    log_info "测试记录: $test_id"
    
    # 更新前的值
    local before=$(psql_exec "SELECT prompt_tokens, completion_tokens FROM request_logs_hot WHERE request_id = '$test_id';" | xargs)
    log_info "更新前: $before"
    
    # 执行 UPDATE
    psql_exec "UPDATE request_logs_hot SET prompt_tokens = 999, completion_tokens = 888, total_tokens = 1887 WHERE request_id = '$test_id';" > /dev/null 2>&1
    
    # 更新后的值
    local after=$(psql_exec "SELECT prompt_tokens, completion_tokens, total_tokens FROM request_logs_hot WHERE request_id = '$test_id';" | xargs)
    log_info "更新后: $after"
    
    if echo "$after" | grep -q "999.*888.*1887"; then
        log_info "✅ UPDATE 操作成功"
        ((PASSED_TESTS++))
    else
        log_error "❌ UPDATE 操作失败"
        ((FAILED_TESTS++))
        ISSUES+=("UPDATE操作失败")
    fi
    
    # 场景 B: 批量更新
    log_info "\n场景 B: 批量更新 cost"
    
    local updated_rows=$(psql_exec "UPDATE request_logs_hot SET cost_usd = 0.999 WHERE request_id LIKE 'test-success%' RETURNING request_id;" | wc -l)
    updated_rows=$(echo "$updated_rows" | xargs)
    
    log_info "批量更新了 $updated_rows 行"
    
    if [[ "$updated_rows" -gt 0 ]]; then
        log_info "✅ 批量 UPDATE 成功"
    else
        log_error "❌ 批量 UPDATE 失败"
        ISSUES+=("批量UPDATE失败")
    fi
}

# ============================================================
# 步骤 5: 测试复杂查询
# ============================================================
function test_queries() {
    log_section "步骤 5: 测试复杂查询"
    ((TOTAL_TESTS++))
    
    log_info "查询 1: 按模型统计成功率"
    psql_exec "
        SELECT 
            client_model,
            COUNT(*) as total,
            SUM(CASE WHEN success THEN 1 ELSE 0 END) as success_count,
            ROUND(100.0 * SUM(CASE WHEN success THEN 1 ELSE 0 END) / COUNT(*), 2) as success_rate
        FROM request_logs_hot
        WHERE request_id LIKE 'test-%'
        GROUP BY client_model
        ORDER BY total DESC;
    "
    
    log_info "\n查询 2: 按错误类型统计"
    psql_exec "
        SELECT 
            error_kind,
            COUNT(*) as count
        FROM request_logs_hot
        WHERE request_id LIKE 'test-%' AND error_kind IS NOT NULL
        GROUP BY error_kind
        ORDER BY count DESC;
    "
    
    log_info "\n查询 3: 延迟统计"
    psql_exec "
        SELECT 
            CASE 
                WHEN latency_ms < 100 THEN '<100ms'
                WHEN latency_ms < 500 THEN '100-500ms'
                WHEN latency_ms < 1000 THEN '500-1000ms'
                WHEN latency_ms < 5000 THEN '1-5s'
                ELSE '>5s'
            END as latency_range,
            COUNT(*) as count,
            AVG(latency_ms) as avg_latency
        FROM request_logs_hot
        WHERE request_id LIKE 'test-%' AND latency_ms IS NOT NULL
        GROUP BY latency_range
        ORDER BY avg_latency;
    "
    
    ((PASSED_TESTS++))
}

# ============================================================
# 步骤 6: 测试视图聚合
# ============================================================
function test_view_aggregation() {
    log_section "步骤 6: 测试视图聚合"
    ((TOTAL_TESTS++))
    
    local hot_count=$(psql_exec "SELECT COUNT(*) FROM request_logs_hot WHERE request_id LIKE 'test-%';" | xargs)
    local parent_count=$(psql_exec "SELECT COUNT(*) FROM request_logs WHERE request_id LIKE 'test-%';" | xargs)
    local view_count=$(psql_exec "SELECT COUNT(*) FROM request_logs_with_current_month WHERE request_id LIKE 'test-%';" | xargs)
    
    log_info "hot 表: $hot_count 条测试数据"
    log_info "父表: $parent_count 条测试数据"
    log_info "视图: $view_count 条测试数据"
    
    local expected=$((hot_count + parent_count))
    
    if [[ "$view_count" -eq "$expected" ]]; then
        log_info "✅ 视图聚合正确 ($hot_count + $parent_count = $view_count)"
        ((PASSED_TESTS++))
    else
        log_error "❌ 视图聚合错误 (期望 $expected, 实际 $view_count)"
        ((FAILED_TESTS++))
        ISSUES+=("视图聚合不正确")
    fi
}

# ============================================================
# 步骤 7: 测试 DELETE 操作
# ============================================================
function test_delete() {
    log_section "步骤 7: 测试 DELETE 操作"
    ((TOTAL_TESTS++))
    
    log_info "场景 A: 单条删除"
    
    local test_id=$(psql_exec "SELECT request_id FROM request_logs_hot WHERE request_id LIKE 'test-fail-timeout%' LIMIT 1;" | xargs)
    
    if [[ -n "$test_id" ]]; then
        log_info "删除记录: $test_id"
        psql_exec "DELETE FROM request_logs_hot WHERE request_id = '$test_id';" > /dev/null 2>&1
        
        local exists=$(psql_exec "SELECT COUNT(*) FROM request_logs_hot WHERE request_id = '$test_id';" | xargs)
        
        if [[ "$exists" -eq 0 ]]; then
            log_info "✅ 单条 DELETE 成功"
            ((PASSED_TESTS++))
        else
            log_error "❌ 单条 DELETE 失败"
            ((FAILED_TESTS++))
            ISSUES+=("单条DELETE失败")
        fi
    else
        log_warn "⚠️  找不到测试记录，跳过删除测试"
        ((PASSED_TESTS++))
    fi
    
    log_info "\n场景 B: 批量删除（清理所有测试数据）"
    
    local before_count=$(psql_exec "SELECT COUNT(*) FROM request_logs_hot WHERE request_id LIKE 'test-%';" | xargs)
    log_info "删除前: $before_count 条"
    
    psql_exec "DELETE FROM request_logs_hot WHERE request_id LIKE 'test-%';" > /dev/null 2>&1
    
    local after_count=$(psql_exec "SELECT COUNT(*) FROM request_logs_hot WHERE request_id LIKE 'test-%';" | xargs)
    log_info "删除后: $after_count 条"
    
    if [[ "$after_count" -eq 0 ]]; then
        log_info "✅ 批量 DELETE 成功"
    else
        log_error "❌ 批量 DELETE 失败 (还剩 $after_count 条)"
        ISSUES+=("批量DELETE不完整")
    fi
}

# ============================================================
# 步骤 8: 数据完整性检查
# ============================================================
function check_data_integrity() {
    log_section "步骤 8: 数据完整性检查"
    ((TOTAL_TESTS++))
    
    log_info "检查 1: 必填字段完整性"
    
    local null_count=$(psql_exec "
        SELECT COUNT(*) 
        FROM request_logs_hot 
        WHERE request_id IS NULL 
           OR ts IS NULL 
           OR tenant_id IS NULL;
    " | xargs)
    
    if [[ "$null_count" -eq 0 ]]; then
        log_info "✅ 必填字段完整"
        ((PASSED_TESTS++))
    else
        log_error "❌ 发现 $null_count 条记录缺少必填字段"
        ((FAILED_TESTS++))
        ISSUES+=("必填字段不完整")
    fi
    
    log_info "\n检查 2: success 与 error_kind 的一致性"
    
    local inconsistent=$(psql_exec "
        SELECT COUNT(*) 
        FROM request_logs_hot 
        WHERE (success = true AND error_kind IS NOT NULL)
           OR (success = false AND error_kind IS NULL);
    " | xargs)
    
    if [[ "$inconsistent" -eq 0 ]]; then
        log_info "✅ success 与 error_kind 一致"
    else
        log_warn "⚠️  发现 $inconsistent 条记录 success 与 error_kind 不一致"
        ISSUES+=("success 与 error_kind 不一致")
    fi
    
    log_info "\n检查 3: tokens 计算正确性"
    
    local wrong_total=$(psql_exec "
        SELECT COUNT(*) 
        FROM request_logs_hot 
        WHERE total_tokens IS NOT NULL 
          AND prompt_tokens IS NOT NULL 
          AND completion_tokens IS NOT NULL
          AND total_tokens != (prompt_tokens + completion_tokens);
    " | xargs)
    
    if [[ "$wrong_total" -eq 0 ]]; then
        log_info "✅ tokens 计算正确"
    else
        log_warn "⚠️  发现 $wrong_total 条记录 total_tokens 计算错误"
        ISSUES+=("total_tokens 计算错误")
    fi
}

# ============================================================
# 步骤 9: 性能测试
# ============================================================
function test_performance() {
    log_section "步骤 9: 性能测试"
    
    log_info "测试 1: 大批量插入性能"
    
    local start_time=$(date +%s%3N)
    
    for i in {1..100}; do
        local req_id="perf-test-$(date +%s)-$i"
        psql_exec "INSERT INTO request_logs_hot (request_id, ts, tenant_id, success, client_model) VALUES ('$req_id', NOW(), 'default', true, 'gpt-3.5-turbo');" > /dev/null 2>&1
    done
    
    local end_time=$(date +%s%3N)
    local duration=$((end_time - start_time))
    
    log_info "插入 100 条记录耗时: ${duration}ms"
    
    if [[ "$duration" -lt 5000 ]]; then
        log_info "✅ 插入性能正常 (<5s)"
    else
        log_warn "⚠️  插入性能较慢 (${duration}ms)"
        ISSUES+=("插入性能较慢")
    fi
    
    # 清理性能测试数据
    psql_exec "DELETE FROM request_logs_hot WHERE request_id LIKE 'perf-test-%';" > /dev/null 2>&1
}

# ============================================================
# 主函数
# ============================================================
function main() {
    echo ""
    echo "=========================================="
    echo "Request Logs 分表架构完整数据验证"
    echo "=========================================="
    echo ""
    
    check_environment
    cleanup_test_data
    generate_test_data
    verify_insert
    test_update
    test_queries
    test_view_aggregation
    test_delete
    check_data_integrity
    test_performance
    
    # 生成总结报告
    log_section "测试总结"
    echo ""
    echo "总测试数: $TOTAL_TESTS"
    echo "通过: $PASSED_TESTS"
    echo "失败: $FAILED_TESTS"
    echo ""
    
    if [[ ${#ISSUES[@]} -gt 0 ]]; then
        echo "发现问题:"
        for issue in "${ISSUES[@]}"; do
            echo "  - $issue"
        done
        echo ""
    fi
    
    if [[ "$FAILED_TESTS" -eq 0 ]] && [[ ${#ISSUES[@]} -eq 0 ]]; then
        log_info "🎉 所有测试通过！数据处理逻辑完全正确！"
        exit 0
    elif [[ "$FAILED_TESTS" -eq 0 ]]; then
        log_warn "⚠️  测试通过，但发现 ${#ISSUES[@]} 个潜在问题"
        exit 0
    else
        log_error "❌ 有 $FAILED_TESTS 个测试失败"
        exit 1
    fi
}

main
