#!/bin/bash
# 分表架构审计脚本
# 功能：审计所有分区表的 hot 表、分区表、视图、索引、代码映射
# 作者：llm-gateway-ops
# 日期：2026-07-06

set -e

# 数据库连接信息（优先使用环境变量）
DB_HOST="${DB_HOST:-10.43.118.61}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_PASSWORD="${DB_PASSWORD:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 日志函数
function log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
function log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
function log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
function log_section() { echo -e "\n${BLUE}========================================${NC}"; echo -e "${BLUE}$1${NC}"; echo -e "${BLUE}========================================${NC}"; }

# 数据库执行函数
function psql_exec() {
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "$1" 2>/dev/null
}

# 审计结果统计
TOTAL_CHECKS=0
PASSED_CHECKS=0
FAILED_CHECKS=0
WARNINGS=0

# 需要审计的表列表（根据迁移文件识别）
PARTITIONED_TABLES=(
    "request_logs"
    "usage_ledger"
    "credit_ledger"
    "tool_usage_stats"
    "request_wal"
    "routing_decision_log"
    "credential_model_index"
    "request_logs_bodies"
)

# 审计报告文件
REPORT_FILE="audit-report-$(date +%Y%m%d-%H%M%S).md"

# 初始化报告
function init_report() {
    cat > "$REPORT_FILE" <<EOF
# 分表架构审计报告

**生成时间**: $(date '+%Y-%m-%d %H:%M:%S')
**数据库**: $DB_HOST:$DB_PORT/$DB_NAME
**审计范围**: ${#PARTITIONED_TABLES[@]} 张分区表

---

EOF
}

# 写入报告
function write_report() {
    echo "$1" >> "$REPORT_FILE"
}

# ============================================================
# 测试 1: 验证 hot 表存在并且是 heap 存储
# ============================================================
function test_hot_tables() {
    log_section "测试 1: 验证 hot 表架构"
    write_report "## 1. Hot 表架构检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        local hot_table="${table}_hot"
        ((TOTAL_CHECKS++))
        
        # 检查表是否存在
        local exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = '$hot_table' AND relnamespace = 'public'::regnamespace);")
        
        if [[ "$exists" =~ "t" ]]; then
            # 检查存储引擎
            local storage=$(psql_exec "SELECT COALESCE(am.amname, 'heap') FROM pg_class c LEFT JOIN pg_am am ON c.relam = am.oid WHERE c.relname = '$hot_table';")
            storage=$(echo "$storage" | xargs)
            
            if [[ "$storage" == "heap" ]]; then
                log_info "✓ $hot_table 存在且为 heap 存储"
                write_report "- ✅ **${hot_table}**: 存在，存储引擎=heap\n"
                ((PASSED_CHECKS++))
            else
                log_error "✗ $hot_table 存储引擎错误: $storage (期望 heap)"
                write_report "- ❌ **${hot_table}**: 存储引擎=$storage (期望 heap)\n"
                ((FAILED_CHECKS++))
            fi
        else
            log_error "✗ $hot_table 不存在"
            write_report "- ❌ **${hot_table}**: 表不存在\n"
            ((FAILED_CHECKS++))
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 2: 验证父表和分区表结构
# ============================================================
function test_parent_tables() {
    log_section "测试 2: 验证父表和分区结构"
    write_report "## 2. 父表和分区结构检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        ((TOTAL_CHECKS++))
        
        # 检查父表是否是分区表
        local is_partitioned=$(psql_exec "SELECT relkind = 'p' FROM pg_class WHERE relname = '$table' AND relnamespace = 'public'::regnamespace;")
        
        if [[ "$is_partitioned" =~ "t" ]]; then
            # 统计 ATTACHED 分区数量
            local partition_count=$(psql_exec "SELECT COUNT(*) FROM pg_inherits i JOIN pg_class c ON i.inhrelid = c.oid WHERE i.inhparent = '$table'::regclass;")
            partition_count=$(echo "$partition_count" | xargs)
            
            log_info "✓ $table 是分区表，包含 $partition_count 个 ATTACHED 分区"
            write_report "- ✅ **${table}**: 分区表，包含 ${partition_count} 个分区\n"
            ((PASSED_CHECKS++))
        else
            log_error "✗ $table 不是分区表或不存在"
            write_report "- ❌ **${table}**: 不是分区表\n"
            ((FAILED_CHECKS++))
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 3: 验证视图存在
# ============================================================
function test_views() {
    log_section "测试 3: 验证聚合视图"
    write_report "## 3. 聚合视图检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        local view_name="${table}_with_current_month"
        ((TOTAL_CHECKS++))
        
        local exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = '$view_name');")
        
        if [[ "$exists" =~ "t" ]]; then
            log_info "✓ $view_name 存在"
            write_report "- ✅ **${view_name}**: 存在\n"
            ((PASSED_CHECKS++))
        else
            log_warn "⚠ $view_name 不存在"
            write_report "- ⚠️  **${view_name}**: 不存在（建议创建）\n"
            ((WARNINGS++))
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 4: 验证 promote 函数存在
# ============================================================
function test_promote_functions() {
    log_section "测试 4: 验证 promote 函数"
    write_report "## 4. Promote 函数检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        local fn_name="promote_${table}_hot_to_partition"
        ((TOTAL_CHECKS++))
        
        local exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = '$fn_name');")
        
        if [[ "$exists" =~ "t" ]]; then
            log_info "✓ $fn_name 存在"
            write_report "- ✅ **${fn_name}**: 存在\n"
            ((PASSED_CHECKS++))
        else
            log_warn "⚠ $fn_name 不存在"
            write_report "- ⚠️  **${fn_name}**: 不存在（建议创建）\n"
            ((WARNINGS++))
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 5: 验证索引完整性
# ============================================================
function test_indexes() {
    log_section "测试 5: 验证索引完整性"
    write_report "## 5. 索引完整性检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        local hot_table="${table}_hot"
        
        # 检查 hot 表的索引数量
        local index_count=$(psql_exec "SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'public' AND tablename = '$hot_table';")
        index_count=$(echo "$index_count" | xargs)
        
        if [[ "$index_count" -gt 0 ]]; then
            log_info "✓ $hot_table 有 $index_count 个索引"
            
            # 列出索引详情
            local indexes=$(psql_exec "SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = '$hot_table' ORDER BY indexname;")
            write_report "- ✅ **${hot_table}**: ${index_count} 个索引\n"
            write_report "  \`\`\`\n$(echo "$indexes" | sed 's/^/  /')\n  \`\`\`\n"
        else
            log_warn "⚠ $hot_table 没有索引"
            write_report "- ⚠️  **${hot_table}**: 没有索引\n"
            ((WARNINGS++))
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 6: 验证 default 分区已删除
# ============================================================
function test_default_partitions_removed() {
    log_section "测试 6: 验证 default 分区已删除"
    write_report "## 6. Default 分区清理检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        local default_table="${table}_default"
        ((TOTAL_CHECKS++))
        
        local exists=$(psql_exec "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = '$default_table');")
        
        if [[ "$exists" =~ "f" ]]; then
            log_info "✓ $default_table 已删除"
            write_report "- ✅ **${default_table}**: 已删除\n"
            ((PASSED_CHECKS++))
        else
            log_warn "⚠ $default_table 仍然存在（应该迁移到 ${table}_hot）"
            write_report "- ⚠️  **${default_table}**: 仍然存在（建议迁移）\n"
            ((WARNINGS++))
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 7: 扫描代码中的 INSERT/UPDATE/DELETE 操作
# ============================================================
function test_code_write_operations() {
    log_section "测试 7: 扫描代码写操作"
    write_report "## 7. 代码写操作检查\n"
    
    write_report "### 7.1 INSERT 操作\n"
    for table in "${PARTITIONED_TABLES[@]}"; do
        local hot_table="${table}_hot"
        local parent_insert=$(grep -r "INSERT INTO ${table}" domains/ --include="*.go" 2>/dev/null | grep -v "_hot" | grep -v "_default" | wc -l)
        local hot_insert=$(grep -r "INSERT INTO ${hot_table}" domains/ --include="*.go" 2>/dev/null | wc -l)
        
        if [[ "$hot_insert" -gt 0 ]]; then
            log_info "✓ ${table}: ${hot_insert} 个 INSERT 指向 ${hot_table}"
            write_report "- ✅ **${table}**: ${hot_insert} 个 INSERT → ${hot_table}\n"
        else
            if [[ "$parent_insert" -gt 0 ]]; then
                log_error "✗ ${table}: ${parent_insert} 个 INSERT 指向父表（应该指向 ${hot_table}）"
                write_report "- ❌ **${table}**: ${parent_insert} 个 INSERT → 父表（应改为 ${hot_table}）\n"
                ((FAILED_CHECKS++))
            else
                log_warn "⚠ ${table}: 未找到 INSERT 操作"
                write_report "- ⚠️  **${table}**: 未找到 INSERT 操作\n"
            fi
        fi
    done
    
    write_report "\n### 7.2 UPDATE 操作\n"
    for table in "${PARTITIONED_TABLES[@]}"; do
        local hot_table="${table}_hot"
        local parent_update=$(grep -r "UPDATE ${table}" domains/ --include="*.go" 2>/dev/null | grep -v "_hot" | grep -v "_default" | wc -l)
        local hot_update=$(grep -r "UPDATE ${hot_table}" domains/ --include="*.go" 2>/dev/null | wc -l)
        
        if [[ "$hot_update" -gt 0 ]]; then
            log_info "✓ ${table}: ${hot_update} 个 UPDATE 指向 ${hot_table}"
            write_report "- ✅ **${table}**: ${hot_update} 个 UPDATE → ${hot_table}\n"
        else
            if [[ "$parent_update" -gt 0 ]]; then
                log_warn "⚠ ${table}: ${parent_update} 个 UPDATE 指向父表（可能需要改为 ${hot_table}）"
                write_report "- ⚠️  **${table}**: ${parent_update} 个 UPDATE → 父表（检查是否需要改为 ${hot_table}）\n"
                ((WARNINGS++))
            fi
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 8: 扫描代码中的 SELECT 操作
# ============================================================
function test_code_read_operations() {
    log_section "测试 8: 扫描代码读操作"
    write_report "## 8. 代码读操作检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        local hot_table="${table}_hot"
        local view_name="${table}_with_current_month"
        
        local hot_select=$(grep -r "FROM ${hot_table}" domains/ --include="*.go" 2>/dev/null | wc -l)
        local view_select=$(grep -r "FROM ${view_name}" domains/ --include="*.go" 2>/dev/null | wc -l)
        local parent_select=$(grep -r "FROM ${table}" domains/ --include="*.go" 2>/dev/null | grep -v "_hot" | grep -v "_with_current_month" | wc -l)
        
        write_report "- **${table}**:\n"
        write_report "  - 查询 ${hot_table}: ${hot_select} 次\n"
        write_report "  - 查询 ${view_name}: ${view_select} 次\n"
        write_report "  - 查询父表: ${parent_select} 次\n"
        
        if [[ "$hot_select" -gt 0 ]] || [[ "$view_select" -gt 0 ]]; then
            log_info "✓ ${table}: 使用 hot 表或视图进行查询"
        else
            log_warn "⚠ ${table}: 可能使用父表直接查询（建议使用 hot 或视图）"
        fi
    done
    write_report "\n"
}

# ============================================================
# 测试 9: 数据一致性检查
# ============================================================
function test_data_consistency() {
    log_section "测试 9: 数据一致性检查"
    write_report "## 9. 数据一致性检查\n"
    
    for table in "${PARTITIONED_TABLES[@]}"; do
        local hot_table="${table}_hot"
        
        # 检查 hot 表数据量
        local hot_count=$(psql_exec "SELECT COUNT(*) FROM ${hot_table};")
        hot_count=$(echo "$hot_count" | xargs)
        
        # 检查父表数据量
        local parent_count=$(psql_exec "SELECT COUNT(*) FROM ${table};")
        parent_count=$(echo "$parent_count" | xargs)
        
        log_info "✓ ${table}: hot 表 ${hot_count} 行，父表 ${parent_count} 行"
        write_report "- **${table}**: hot=${hot_count} 行，父表=${parent_count} 行\n"
    done
    write_report "\n"
}

# ============================================================
# 主函数
# ============================================================
function main() {
    log_section "开始分表架构审计"
    
    init_report
    
    test_hot_tables
    test_parent_tables
    test_views
    test_promote_functions
    test_indexes
    test_default_partitions_removed
    test_code_write_operations
    test_code_read_operations
    test_data_consistency
    
    # 生成总结
    log_section "审计总结"
    write_report "## 10. 审计总结\n"
    write_report "- **总检查项**: ${TOTAL_CHECKS}\n"
    write_report "- **通过**: ${PASSED_CHECKS}\n"
    write_report "- **失败**: ${FAILED_CHECKS}\n"
    write_report "- **警告**: ${WARNINGS}\n"
    write_report "\n"
    
    if [[ "$FAILED_CHECKS" -eq 0 ]]; then
        log_info "✅ 所有关键检查通过！"
        write_report "### ✅ 结论：架构基本正常\n"
    else
        log_error "❌ 发现 ${FAILED_CHECKS} 个严重问题，需要修复"
        write_report "### ❌ 结论：发现严重问题，需要修复\n"
    fi
    
    if [[ "$WARNINGS" -gt 0 ]]; then
        log_warn "⚠  发现 ${WARNINGS} 个警告，建议优化"
        write_report "### ⚠️  建议：优化 ${WARNINGS} 个警告项\n"
    fi
    
    log_info "详细报告已保存到: $REPORT_FILE"
}

main
