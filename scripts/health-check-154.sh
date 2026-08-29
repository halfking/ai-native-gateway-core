#!/bin/bash
# 
# 154 生产环境健康检查脚本
# 
# 功能: 检查服务健康状态、关键指标和 SSE 验证功能
# 作者: DevOps Team
# 日期: 2026-08-29
# 版本: v1.0
#

set -euo pipefail

# ==================== 配置区 ====================

# 生产环境配置
PROD_HOST="${PROD_HOST:-154.XXX.XXX.XXX}"  # 请设置实际 IP
PROD_PORT="${PROD_PORT:-22}"
PROD_USER="${PROD_USER:-root}"
SERVICE_NAME="llmgo-154.service"
API_PORT="8781"

# 数据库配置
DB_HOST="172.16.2.210"
DB_PORT="5432"
DB_NAME="llm_gateway"
DB_USER="llm_gateway"
DB_PASS="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"

# 检查时间窗口（分钟）
TIME_WINDOW="${TIME_WINDOW:-10}"

# 阈值配置
SUCCESS_RATE_THRESHOLD=85
MALFORMED_RATE_THRESHOLD=5
ERROR_LOG_THRESHOLD=10

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# 检查结果统计
CHECKS_PASSED=0
CHECKS_FAILED=0
CHECKS_WARNING=0

# ==================== 函数定义 ====================

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[✓ PASS]${NC} $*"
    ((CHECKS_PASSED++))
}

log_warning() {
    echo -e "${YELLOW}[⚠ WARN]${NC} $*"
    ((CHECKS_WARNING++))
}

log_error() {
    echo -e "${RED}[✗ FAIL]${NC} $*"
    ((CHECKS_FAILED++))
}

log_section() {
    echo ""
    echo -e "${CYAN}========================================${NC}"
    echo -e "${CYAN}$*${NC}"
    echo -e "${CYAN}========================================${NC}"
}

# 执行远程命令
remote_exec() {
    ssh -p "$PROD_PORT" "$PROD_USER@$PROD_HOST" "$@" 2>/dev/null
}

# 检查命令是否存在
check_command() {
    if ! command -v "$1" &> /dev/null; then
        log_error "命令 '$1' 未找到"
        return 1
    fi
}

# ==================== 检查函数 ====================

# 1. 服务状态检查
check_service_status() {
    log_section "1. 服务状态检查"
    
    # 检查服务是否运行
    if remote_exec "systemctl is-active $SERVICE_NAME" &>/dev/null; then
        log_success "服务状态: running"
    else
        log_error "服务状态: not running"
        return 1
    fi
    
    # 获取服务运行时长
    local uptime=$(remote_exec "systemctl show $SERVICE_NAME --property=ActiveEnterTimestamp --value" || echo "Unknown")
    if [[ "$uptime" != "Unknown" ]]; then
        log_info "服务启动时间: $uptime"
    fi
    
    # 检查服务是否有错误
    local failed_count=$(remote_exec "systemctl show $SERVICE_NAME --property=NRestarts --value" || echo "0")
    if [[ $failed_count -eq 0 ]]; then
        log_success "服务重启次数: $failed_count"
    else
        log_warning "服务重启次数: $failed_count"
    fi
}

# 2. API 健康检查
check_api_health() {
    log_section "2. API 健康检查"
    
    # 检查版本接口
    log_info "检查版本接口..."
    local version_response=$(curl -s -m 5 "http://${PROD_HOST}:${API_PORT}/api/system/version" || echo "")
    if [[ -n "$version_response" ]]; then
        log_success "版本接口: 可访问"
        echo "$version_response" | jq -r '. | "  版本: \(.version // "unknown"), 构建时间: \(.build_time // "unknown")"' 2>/dev/null || echo "  $version_response"
    else
        log_error "版本接口: 无响应"
    fi
    
    # 检查健康接口
    log_info "检查健康接口..."
    local health_response=$(curl -s -m 5 "http://${PROD_HOST}:${API_PORT}/api/system/health" || echo "")
    if [[ -n "$health_response" ]] && echo "$health_response" | grep -q "healthy"; then
        log_success "健康检查: healthy"
    else
        log_error "健康检查: unhealthy 或无响应"
        [[ -n "$health_response" ]] && echo "  响应: $health_response"
    fi
}

# 3. 数据库连接检查
check_database_connection() {
    log_section "3. 数据库连接检查"
    
    log_info "测试数据库连接..."
    if remote_exec "PGPASSWORD='$DB_PASS' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c 'SELECT 1;'" &>/dev/null; then
        log_success "数据库连接: 正常"
    else
        log_error "数据库连接: 失败"
        return 1
    fi
}

# 4. 请求统计检查
check_request_statistics() {
    log_section "4. 请求统计检查（最近 ${TIME_WINDOW} 分钟）"
    
    log_info "查询请求统计..."
    local stats=$(remote_exec "PGPASSWORD='$DB_PASS' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -A -F'|'" <<EOF
SELECT 
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE success = true) AS success,
    COUNT(*) FILTER (WHERE success = false) AS failed,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / NULLIF(COUNT(*), 0), 2) AS rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '$TIME_WINDOW minutes';
EOF
)
    
    if [[ -n "$stats" ]]; then
        IFS='|' read -r total success failed rate <<< "$stats"
        
        echo "  总请求数: $total"
        echo "  成功请求: $success"
        echo "  失败请求: $failed"
        echo "  成功率: ${rate}%"
        
        if [[ $total -eq 0 ]]; then
            log_warning "请求统计: 无请求数据"
        elif (( $(echo "$rate >= $SUCCESS_RATE_THRESHOLD" | bc -l) )); then
            log_success "成功率: ${rate}% (>= ${SUCCESS_RATE_THRESHOLD}%)"
        elif (( $(echo "$rate >= 70" | bc -l) )); then
            log_warning "成功率: ${rate}% (< ${SUCCESS_RATE_THRESHOLD}%)"
        else
            log_error "成功率: ${rate}% (严重低于阈值)"
        fi
    else
        log_warning "无法获取请求统计"
    fi
}

# 5. SSE 验证检查
check_sse_validation() {
    log_section "5. SSE 验证功能检查（最近 ${TIME_WINDOW} 分钟）"
    
    # 检查 malformed_sse_frame 日志
    log_info "检查 malformed SSE frame 日志..."
    local malformed_count=$(remote_exec "journalctl -u $SERVICE_NAME --since '${TIME_WINDOW} minutes ago' --no-pager | grep -c 'malformed.*sse.*frame' || echo 0")
    
    echo "  Malformed SSE 帧数: $malformed_count"
    
    if [[ $malformed_count -eq 0 ]]; then
        log_success "SSE 验证: 无 malformed 帧"
    elif [[ $malformed_count -lt 10 ]]; then
        log_warning "SSE 验证: 发现 $malformed_count 个 malformed 帧"
    else
        log_error "SSE 验证: 发现 $malformed_count 个 malformed 帧（过多）"
    fi
    
    # 检查 MiniMax 和 GLM 模型请求
    log_info "检查 MiniMax/GLM 模型请求..."
    local provider_stats=$(remote_exec "PGPASSWORD='$DB_PASS' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -A -F'|'" <<EOF
SELECT 
    COALESCE(outbound_model, client_model) AS model,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE success = true) AS success,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) AS rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '$TIME_WINDOW minutes'
  AND (outbound_model LIKE '%minimax%' OR outbound_model LIKE '%glm%' 
       OR client_model LIKE '%minimax%' OR client_model LIKE '%glm%')
GROUP BY COALESCE(outbound_model, client_model)
ORDER BY total DESC
LIMIT 5;
EOF
)
    
    if [[ -n "$provider_stats" ]]; then
        echo "  高风险提供商统计:"
        while IFS='|' read -r model total success rate; do
            echo "    - $model: $total 请求, 成功率 ${rate}%"
        done <<< "$provider_stats"
        log_success "MiniMax/GLM 模型: 有数据"
    else
        log_info "MiniMax/GLM 模型: 无请求数据"
    fi
}

# 6. 错误日志检查
check_error_logs() {
    log_section "6. 错误日志检查（最近 ${TIME_WINDOW} 分钟）"
    
    log_info "统计错误日志..."
    local error_count=$(remote_exec "journalctl -u $SERVICE_NAME --since '${TIME_WINDOW} minutes ago' --no-pager | grep -c -E '(ERROR|FATAL)' || echo 0")
    
    echo "  错误日志数量: $error_count"
    
    if [[ $error_count -eq 0 ]]; then
        log_success "错误日志: 无 ERROR/FATAL"
    elif [[ $error_count -lt $ERROR_LOG_THRESHOLD ]]; then
        log_warning "错误日志: $error_count 条（可接受）"
    else
        log_error "错误日志: $error_count 条（过多）"
    fi
    
    # 显示最近的错误（如果有）
    if [[ $error_count -gt 0 && $error_count -le 5 ]]; then
        log_info "最近的错误日志:"
        remote_exec "journalctl -u $SERVICE_NAME --since '${TIME_WINDOW} minutes ago' --no-pager | grep -E '(ERROR|FATAL)' | tail -5" | sed 's/^/  /'
    fi
}

# 7. 系统资源检查
check_system_resources() {
    log_section "7. 系统资源检查"
    
    # CPU 使用率
    log_info "检查 CPU 使用率..."
    local cpu_usage=$(remote_exec "top -bn1 | grep 'Cpu(s)' | awk '{print \$2}' | cut -d'%' -f1" || echo "0")
    echo "  CPU 使用率: ${cpu_usage}%"
    if (( $(echo "$cpu_usage < 80" | bc -l) )); then
        log_success "CPU 使用率: 正常"
    else
        log_warning "CPU 使用率: 较高 (${cpu_usage}%)"
    fi
    
    # 内存使用率
    log_info "检查内存使用率..."
    local mem_usage=$(remote_exec "free | grep Mem | awk '{printf \"%.1f\", \$3/\$2 * 100.0}'" || echo "0")
    echo "  内存使用率: ${mem_usage}%"
    if (( $(echo "$mem_usage < 85" | bc -l) )); then
        log_success "内存使用率: 正常"
    elif (( $(echo "$mem_usage < 95" | bc -l) )); then
        log_warning "内存使用率: 较高 (${mem_usage}%)"
    else
        log_error "内存使用率: 过高 (${mem_usage}%)"
    fi
    
    # 磁盘使用率
    log_info "检查磁盘使用率..."
    local disk_usage=$(remote_exec "df -h /opt | tail -1 | awk '{print \$5}' | sed 's/%//'" || echo "0")
    echo "  磁盘使用率: ${disk_usage}%"
    if [[ $disk_usage -lt 80 ]]; then
        log_success "磁盘使用率: 正常"
    elif [[ $disk_usage -lt 90 ]]; then
        log_warning "磁盘使用率: 较高 (${disk_usage}%)"
    else
        log_error "磁盘使用率: 过高 (${disk_usage}%)"
    fi
}

# 8. 错误类型分布检查
check_error_distribution() {
    log_section "8. 错误类型分布（最近 ${TIME_WINDOW} 分钟）"
    
    log_info "查询错误类型分布..."
    local error_dist=$(remote_exec "PGPASSWORD='$DB_PASS' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -A -F'|'" <<EOF
SELECT 
    error_kind,
    COUNT(*) AS count
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '$TIME_WINDOW minutes'
  AND success = false
  AND error_kind IS NOT NULL
GROUP BY error_kind
ORDER BY count DESC
LIMIT 10;
EOF
)
    
    if [[ -n "$error_dist" ]]; then
        echo "  Top 错误类型:"
        while IFS='|' read -r error_kind count; do
            echo "    - $error_kind: $count"
            
            # 检查是否有 malformed_sse_frame 错误
            if [[ "$error_kind" == "malformed_sse_frame" ]]; then
                log_warning "发现 malformed_sse_frame 错误: $count 次"
            fi
        done <<< "$error_dist"
        log_success "错误分布: 已统计"
    else
        log_success "错误分布: 无错误"
    fi
}

# ==================== 汇总报告 ====================

print_summary() {
    log_section "健康检查汇总"
    
    local total_checks=$((CHECKS_PASSED + CHECKS_FAILED + CHECKS_WARNING))
    
    echo ""
    echo -e "${GREEN}通过: $CHECKS_PASSED${NC}"
    echo -e "${YELLOW}警告: $CHECKS_WARNING${NC}"
    echo -e "${RED}失败: $CHECKS_FAILED${NC}"
    echo -e "总计: $total_checks"
    echo ""
    
    # 计算健康度
    if [[ $CHECKS_FAILED -eq 0 && $CHECKS_WARNING -eq 0 ]]; then
        echo -e "${GREEN}✓ 系统状态: 健康${NC}"
        return 0
    elif [[ $CHECKS_FAILED -eq 0 ]]; then
        echo -e "${YELLOW}⚠ 系统状态: 基本健康（有警告）${NC}"
        return 1
    else
        echo -e "${RED}✗ 系统状态: 不健康（有失败项）${NC}"
        return 2
    fi
}

# ==================== 主流程 ====================

main() {
    local start_time=$(date +%s)
    
    echo ""
    log_info "=========================================="
    log_info "154 生产环境健康检查"
    log_info "时间: $(date '+%Y-%m-%d %H:%M:%S')"
    log_info "时间窗口: 最近 ${TIME_WINDOW} 分钟"
    log_info "=========================================="
    
    # 检查必要命令
    check_command ssh || exit 1
    check_command curl || exit 1
    
    # 执行所有检查
    check_service_status
    check_api_health
    check_database_connection
    check_request_statistics
    check_sse_validation
    check_error_logs
    check_system_resources
    check_error_distribution
    
    # 打印汇总报告
    print_summary
    local health_status=$?
    
    local end_time=$(date +%s)
    local duration=$((end_time - start_time))
    
    echo ""
    log_info "检查完成，耗时: ${duration} 秒"
    echo ""
    
    # 返回健康状态码
    exit $health_status
}

# ==================== 脚本入口 ====================

# 捕获 Ctrl+C
trap 'echo ""; log_warning "检查被中断"; exit 130' INT

# 执行主流程
main "$@"
