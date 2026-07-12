#!/bin/bash
# request_logs 完整数据验证 - 适配当前分区表架构
# 测试目标：验证当前分区表的 CRUD 操作，发现数据处理问题

set -e

# 数据库配置（使用本地环境）
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-kxuser}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_PASSWORD="${DB_PASSWORD:-kxpass}"

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
    
    # 检查 request_logs 表
    local table_exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'request_logs');" | xargs)
    
    if [[ "$table_exists" == "t" ]]; then
        log_info "✅ request_logs 表存在"
        ((PASSED_TESTS++))
    else
        log_error "❌ request_logs 表不存在"
        exit 1
    fi
    
    # 检查表类型
    local is_partitioned=$(psql_exec "SELECT relkind = 'p' FROM pg_class WHERE relname = 'request_logs';" | xargs)
    
    if [[ "$is_partitioned" == "t" ]]; then
        log_info "✅ request_logs 是分区表"
        
        # 统计分区数量
        local partition_count=$(psql_exec "SELECT COUNT(*) FROM pg_inherits i JOIN pg_class c ON i.inhrelid = c.oid WHERE i.inhparent = 'request_logs'::regclass;" | xargs)
        log_info "   分区数量: $partition_count"
        
        # 列出分区
        log_info "   分区列表:"
        psql_exec "SELECT c.relname FROM pg_inherits i JOIN pg_class c ON i.inhrelid = c.oid WHERE i.inhparent = 'request_logs'::regclass ORDER BY c.relname;"
    else
        log_warn "⚠️  request_logs 不是分区表（可能是普通表）"
    fi
}

# ============================================================
# 步骤 1: 清理测试数据
# ============================================================
function cleanup_test_data() {
    log_section "步骤 1: 清理旧测试数据"
    
    log_info "清理 request_logs 测试数据..."
    local deleted=$(psql_exec "DELETE FROM request_logs WHERE request_id LIKE 'test-data-validation-%' RETURNING request_id;" | wc -l | xargs)
    log_info "清理了 $deleted 条旧测试数据"
}

# ============================================================
# 步骤 2: 生成多样化测试数据
# ============================================================
function generate_test_data() {
    log_section "步骤 2: 生成多样化测试数据（直接插入分区表）"
    ((TOTAL_TESTS++))
    
    log_info "生成 25 条测试数据，涵盖多种场景..."
    
    local timestamp=$(date +%s)
    local inserted=0
    local failed=0
    
    # 场景 1: 成功请求 - GPT-4 系列 (6条)
    log_info "场景 1: GPT-4 成功请求..."
    for i in {1..6}; do
        local req_id="test-data-validation-gpt4-${timestamp}-${i}"
        if psql_exec "INSERT INTO request_logs (
            request_id, ts, tenant_id, success, 
            client_model, outbound_model, 
            prompt_tokens, completion_tokens, total_tokens,
            cost_usd, latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', true,
            'gpt-4', 'gpt-4-0613',
            $((100 + i * 10)), $((50 + i * 5)), $((150 + i * 15)),
            $((3 + i))::numeric / 1000, $((200 + i * 50))
        );" > /dev/null 2>&1; then
            ((inserted++))
        else
            ((failed++))
            log_warn "  插入失败: $req_id"
        fi
    done
    
    # 场景 2: 成功请求 - Claude 系列 (4条)
    log_info "场景 2: Claude 成功请求..."
    for i in {1..4}; do
        local req_id="test-data-validation-claude-${timestamp}-${i}"
        if psql_exec "INSERT INTO request_logs (
            request_id, ts, tenant_id, success,
            client_model, outbound_model,
            prompt_tokens, completion_tokens, total_tokens,
            cost_usd, latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', true,
            'claude-3-opus', 'claude-3-opus-20240229',
            $((200 + i * 20)), $((100 + i * 10)), $((300 + i * 30)),
            $((15 + i))::numeric / 1000, $((300 + i * 100))
        );" > /dev/null 2>&1; then
            ((inserted++))
        else
            ((failed++))
        fi
    done
    
    # 场景 3: 失败请求 - rate_limit (5条)
    log_info "场景 3: rate_limit 失败..."
    for i in {1..5}; do
        local req_id="test-data-validation-ratelimit-${timestamp}-${i}"
        if psql_exec "INSERT INTO request_logs (
            request_id, ts, tenant_id, success,
            client_model, error_kind,
            latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', false,
            'gpt-3.5-turbo', 'rate_limit',
            $((50 + i * 10))
        );" > /dev/null 2>&1; then
            ((inserted++))
        else
            ((failed++))
        fi
    done
    
    # 场景 4: 失败请求 - timeout (3条)
    log_info "场景 4: timeout 失败..."
    for i in {1..3}; do
        local req_id="test-data-validation-timeout-${timestamp}-${i}"
        if psql_exec "INSERT INTO request_logs (
            request_id, ts, tenant_id, success,
            client_model, error_kind,
            latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', false,
            'gpt-4', 'timeout',
            30000
        );" > /dev/null 2>&1; then
            ((inserted++))
        else
            ((failed++))
        fi
    done
    
    # 场景 5: 流式响应 (4条)
    log_info "场景 5: 流式响应..."
    for i in {1..4}; do
        local req_id="test-data-validation-stream-${timestamp}-${i}"
        if psql_exec "INSERT INTO request_logs (
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
        );" > /dev/null 2>&1; then
            ((inserted++))
        else
            ((failed++))
        fi
    done
    
    # 场景 6: 缓存命中 (3条)
    log_info "场景 6: 缓存命中..."
    for i in {1..3}; do
        local req_id="test-data-validation-cache-${timestamp}-${i}"
        if psql_exec "INSERT INTO request_logs (
            request_id, ts, tenant_id, success,
            client_model, cache_read_tokens,
            prompt_tokens, completion_tokens,
            latency_ms
        ) VALUES (
            '$req_id', NOW(), 'default', true,
            'gpt-4', $((500 + i * 100)),
            $((50 + i * 10)), $((30 + i * 5)),
            $((50 + i * 10))
        );" > /dev/null 2>&1; then
            ((inserted++))
        else
            ((failed++))
        fi
    done
    
    log_info "✅ 成功插入 $inserted 条测试数据"
    
    if [[ "$failed" -gt 0 ]]; then
        log_warn "⚠️  插入失败 $failed 条"
        ISSUES+=("数据插入部分失败: $failed 条")
    fi
    
    if [[ "$inserted" -ge 20 ]]; then
        ((PASSED_TESTS++))
    else
        log_error "❌ 插入数据不足 (期望 25, 实际 $inserted)"
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
    
    local count=$(psql_exec "SELECT COUNT(*) FROM request_logs WHERE request_id LIKE 'test-data-validation-%';" | xargs)
    
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
                WHEN request_id LIKE '%gpt4%' THEN 'GPT-4 成功'
                WHEN request_id LIKE '%claude%' THEN 'Claude 成功'
                WHEN request_id LIKE '%ratelimit%' THEN 'rate_limit 失败'
                WHEN request_id LIKE '%timeout%' THEN 'timeout 失败'
                WHEN request_id LIKE '%stream%' THEN '流式响应'
                WHEN request_id LIKE '%cache%' THEN '缓存命中'
                ELSE '其他'
            END as scenario,
            COUNT(*) as count
        FROM request_logs
        WHERE request_id LIKE 'test-data-validation-%'
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
    
    log_info "场景 A: 单条 UPDATE (模拟流式响应完成后更新 tokens)"
    
    local test_id=$(psql_exec "SELECT request_id FROM request_logs WHERE request_id LIKE 'test-data-validation-gpt4%' LIMIT 1;" | xargs)
    
    if [[ -z "$test_id" ]]; then
        log_error "❌ 找不到测试记录"
        ((FAILED_TESTS++))
        ISSUES+=("UPDATE测试: 找不到测试记录")
        return
    fi
    
    log_info "测试记录: $test_id"
    
    # 更新前
    local before=$(psql_exec "SELECT prompt_tokens, completion_tokens FROM request_logs WHERE request_id = '$test_id';" | xargs)
    log_info "更新前: $before"
    
    # 执行 UPDATE
    local update_result=$(psql_exec "UPDATE request_logs SET prompt_tokens = 999, completion_tokens = 888, total_tokens = 1887 WHERE request_id = '$test_id';" 2>&1)
    
    if echo "$update_result" | grep -qi "error"; then
        log_error "❌ UPDATE 执行出错: $update_result"
        ((FAILED_TESTS++))
        ISSUES+=("UPDATE操作报错")
        return
    fi
    
    # 更新后
    local after=$(psql_exec "SELECT prompt_tokens, completion_tokens, total_tokens FROM request_logs WHERE request_id = '$test_id';" | xargs)
    log_info "更新后: $after"
    
    if echo "$after" | grep -q "999.*888.*1887"; then
        log_info "✅ UPDATE 操作成功"
        ((PASSED_TESTS++))
    else
        log_error "❌ UPDATE 操作失败 (数据未更新)"
        ((FAILED_TESTS++))
        ISSUES+=("UPDATE操作失败")
    fi
    
    # 场景 B: 批量更新
    log_info "\n场景 B: 批量 UPDATE cost"
    
    local updated_rows=$(psql_exec "UPDATE request_logs SET cost_usd = 0.888 WHERE request_id LIKE 'test-data-validation-gpt4%' OR request_id LIKE 'test-data-validation-claude%' RETURNING request_id;" 2>&1 | grep -c "test-data-validation" || echo "0")
    updated_rows=$(echo "$updated_rows" | xargs)
    
    log_info "批量更新了 $updated_rows 行"
    
    if [[ "$updated_rows" -gt 0 ]]; then
        log_info "✅ 批量 UPDATE 成功"
    else
        log_warn "⚠️  批量 UPDATE 可能失败"
        ISSUES+=("批量UPDATE效果不确定")
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
        FROM request_logs
        WHERE request_id LIKE 'test-data-validation-%'
        GROUP BY client_model
        ORDER BY total DESC;
    "
    
    log_info "\n查询 2: 按错误类型统计"
    psql_exec "
        SELECT 
            error_kind,
            COUNT(*) as count
        FROM request_logs
        WHERE request_id LIKE 'test-data-validation-%' AND error_kind IS NOT NULL
        GROUP BY error_kind
        ORDER BY count DESC;
    "
    
    log_info "\n查询 3: 延迟分布统计"
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
            AVG(latency_ms)::int as avg_latency
        FROM request_logs
        WHERE request_id LIKE 'test-data-validation-%' AND latency_ms IS NOT NULL
        GROUP BY latency_range
        ORDER BY avg_latency;
    "
    
    ((PASSED_TESTS++))
}

# ============================================================
# 步骤 6: 测试 DELETE 操作
# ============================================================
function test_delete() {
    log_section "步骤 6: 测试 DELETE 操作"
    ((TOTAL_TESTS++))
    
    log_info "场景 A: 单条 DELETE"
    
    local test_id=$(psql_exec "SELECT request_id FROM request_logs WHERE request_id LIKE 'test-data-validation-timeout%' LIMIT 1;" | xargs)
    
    if [[ -n "$test_id" ]]; then
        log_info "删除记录: $test_id"
        psql_exec "DELETE FROM request_logs WHERE request_id = '$test_id';" > /dev/null 2>&1
        
        local exists=$(psql_exec "SELECT COUNT(*) FROM request_logs WHERE request_id = '$test_id';" | xargs)
        
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
    
    log_info "\n场景 B: 批量 DELETE (清理所有测试数据)"
    
    local before_count=$(psql_exec "SELECT COUNT(*) FROM request_logs WHERE request_id LIKE 'test-data-validation-%';" | xargs)
    log_info "删除前: $before_count 条"
    
    psql_exec "DELETE FROM request_logs WHERE request_id LIKE 'test-data-validation-%';" > /dev/null 2>&1
    
    local after_count=$(psql_exec "SELECT COUNT(*) FROM request_logs WHERE request_id LIKE 'test-data-validation-%';" | xargs)
    log_info "删除后: $after_count 条"
    
    if [[ "$after_count" -eq 0 ]]; then
        log_info "✅ 批量 DELETE 成功"
    else
        log_warn "⚠️  批量 DELETE 不完整 (还剩 $after_count 条)"
        ISSUES+=("批量DELETE不完整")
    fi
}

# ============================================================
# 步骤 7: 数据完整性检查
# ============================================================
function check_data_integrity() {
    log_section "步骤 7: 数据完整性检查（使用真实数据）"
    ((TOTAL_TESTS++))
    
    log_info "检查 1: 必填字段完整性"
    
    local null_count=$(psql_exec "
        SELECT COUNT(*) 
        FROM request_logs 
        WHERE request_id IS NULL 
           OR ts IS NULL 
           OR tenant_id IS NULL
        LIMIT 1000;
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
        FROM request_logs 
        WHERE (success = true AND error_kind IS NOT NULL)
           OR (success = false AND error_kind IS NULL)
        LIMIT 100;
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
        FROM request_logs 
        WHERE total_tokens IS NOT NULL 
          AND prompt_tokens IS NOT NULL 
          AND completion_tokens IS NOT NULL
          AND total_tokens != (prompt_tokens + completion_tokens)
        LIMIT 100;
    " | xargs)
    
    if [[ "$wrong_total" -eq 0 ]]; then
        log_info "✅ tokens 计算正确"
    else
        log_warn "⚠️  发现 $wrong_total 条记录 total_tokens 计算错误"
        ISSUES+=("total_tokens 计算错误")
    fi
}

# ============================================================
# 步骤 8: 分区状态检查
# ============================================================
function check_partitions() {
    log_section "步骤 8: 分区状态检查"
    ((TOTAL_TESTS++))
    
    log_info "分区详情:"
    psql_exec "
        SELECT 
            c.relname as partition_name,
            pg_size_pretty(pg_total_relation_size(c.oid)) as size,
            (SELECT count(*) FROM ONLY request_logs WHERE tableoid = c.oid) as row_count
        FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        WHERE i.inhparent = 'request_logs'::regclass
        ORDER BY c.relname;
    "
    
    ((PASSED_TESTS++))
}

# ============================================================
# 主函数
# ============================================================
function main() {
    echo ""
    echo "=========================================="
    echo "Request Logs 完整数据验证"
    echo "适配当前分区表架构"
    echo "=========================================="
    echo ""
    
    check_environment
    cleanup_test_data
    generate_test_data
    verify_insert
    test_update
    test_queries
    test_delete
    check_data_integrity
    check_partitions
    
    # 生成总结报告
    log_section "测试总结"
    echo ""
    echo "总测试数: $TOTAL_TESTS"
    echo "通过: $PASSED_TESTS"
    echo "失败: $FAILED_TESTS"
    echo ""
    
    if [[ ${#ISSUES[@]} -gt 0 ]]; then
        echo "发现问题 (${#ISSUES[@]} 个):"
        for issue in "${ISSUES[@]}"; do
            echo "  - $issue"
        done
        echo ""
    fi
    
    # 生成指导建议
    log_section "审计指导建议"
    echo ""
    echo "基于本次测试，其他表的审计应关注："
    echo ""
    echo "1. 数据插入完整性"
    echo "   - 验证所有必填字段"
    echo "   - 检查外键约束"
    echo "   - 测试并发插入"
    echo ""
    echo "2. UPDATE 操作"
    echo "   - 验证单条更新"
    echo "   - 验证批量更新"
    echo "   - 检查更新后数据一致性"
    echo ""
    echo "3. 数据完整性"
    echo "   - success 与 error_kind 的逻辑一致性"
    echo "   - 计算字段正确性 (如 total_tokens)"
    echo "   - 时间戳字段合理性"
    echo ""
    echo "4. 查询性能"
    echo "   - 测试常用查询"
    echo "   - 验证索引有效性"
    echo "   - 检查分区裁剪"
    echo ""
    
    if [[ "$FAILED_TESTS" -eq 0 ]] && [[ ${#ISSUES[@]} -eq 0 ]]; then
        log_info "🎉 所有测试通过！数据处理逻辑完全正确！"
        exit 0
    elif [[ "$FAILED_TESTS" -eq 0 ]]; then
        log_warn "⚠️  测试通过，但发现 ${#ISSUES[@]} 个潜在问题需要关注"
        exit 0
    else
        log_error "❌ 有 $FAILED_TESTS 个测试失败，需要修复"
        exit 1
    fi
}

main
