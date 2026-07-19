#!/bin/bash
# ============================================================================
# 自动化测试验证脚本
# Date: 2026-07-19
# Purpose: 验证火山引擎GLM-5.2配置和智谱AI/商汤错误处理问题
# ============================================================================

set -e  # 遇到错误立即退出

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查数据库连接
check_database_connection() {
    log_info "检查数据库连接..."

    if [ -z "$DATABASE_URL" ]; then
        log_error "DATABASE_URL 环境变量未设置"
        echo ""
        echo "请先设置数据库连接，可选方式："
        echo "  1. export DATABASE_URL='postgresql://user:pass@host:port/dbname'"
        echo "  2. source .env"
        echo "  3. source .env.local"
        echo ""
        exit 1
    fi

    if ! psql "$DATABASE_URL" -c "SELECT 1;" > /dev/null 2>&1; then
        log_error "无法连接到数据库"
        echo ""
        echo "请检查 DATABASE_URL 是否正确："
        echo "  $DATABASE_URL"
        echo ""
        exit 1
    fi

    log_success "数据库连接成功"
}

# 执行火山引擎配置
execute_volcano_config() {
    log_info "执行火山引擎 GLM-5.2 配置..."

    local sql_file="sql/migrations/manual/20260719_add_volcano_glm52.sql"

    if [ ! -f "$sql_file" ]; then
        log_error "配置文件不存在: $sql_file"
        exit 1
    fi

    if psql "$DATABASE_URL" -f "$sql_file" > /tmp/volcano_config.log 2>&1; then
        log_success "火山引擎配置完成"

        # 显示配置结果摘要
        echo ""
        echo "=== 配置摘要 ==="
        grep -E "INSERT 0|SELECT" /tmp/volcano_config.log | tail -10
        echo ""
    else
        log_error "配置执行失败"
        echo ""
        cat /tmp/volcano_config.log
        exit 1
    fi
}

# 执行验证脚本
execute_verification() {
    log_info "执行验证脚本..."

    local sql_file="sql/migrations/manual/20260719_verify_config.sql"
    local output_file="/tmp/verify_result_$(date +%Y%m%d_%H%M%S).txt"

    if [ ! -f "$sql_file" ]; then
        log_error "验证脚本不存在: $sql_file"
        exit 1
    fi

    if psql "$DATABASE_URL" -f "$sql_file" > "$output_file" 2>&1; then
        log_success "验证完成，结果保存到: $output_file"
        echo ""

        # 显示关键结果
        display_verification_summary "$output_file"
    else
        log_error "验证执行失败"
        echo ""
        cat "$output_file"
        exit 1
    fi
}

# 显示验证结果摘要
display_verification_summary() {
    local output_file=$1

    echo "================================================================"
    echo "                    验证结果摘要"
    echo "================================================================"
    echo ""

    # 验证6: 智谱AI错误统计
    echo "--- 验证6: 智谱AI错误统计（最关键） ---"
    awk '/验证6:/,/验证7:/' "$output_file" | grep -A 10 "error_kind" | head -15
    echo ""

    # 验证7: 降级模式触发
    echo "--- 验证7: 降级模式触发情况（最关键） ---"
    awk '/验证7:/,/验证完成/' "$output_file" | grep -A 10 "credential_id" | head -15
    echo ""

    # 分析结果
    analyze_results "$output_file"
}

# 分析结果并给出诊断
analyze_results() {
    local output_file=$1

    echo "================================================================"
    echo "                    问题诊断"
    echo "================================================================"
    echo ""

    # 检查验证6的结果
    local has_rate_limit=$(grep -c "rate_limit" "$output_file" || true)
    local has_model_not_found=$(grep -c "model_not_found" "$output_file" || true)

    # 检查验证7的结果（降级模式触发）
    local degraded_usage=$(awk '/验证7:/,/验证完成/' "$output_file" | grep -c "credential_id" || true)

    echo "检测结果："
    echo "  - rate_limit 错误数: $has_rate_limit"
    echo "  - model_not_found 错误数: $has_model_not_found"
    echo "  - 降级模式触发记录数: $degraded_usage"
    echo ""

    # 诊断逻辑
    if [ "$has_rate_limit" -gt 0 ] && [ "$degraded_usage" -gt 0 ]; then
        log_error "【问题确认】降级模式正在强制使用限流的凭证！"
        echo ""
        echo "问题根因："
        echo "  - 错误分类正确（rate_limit）"
        echo "  - 但降级模式将 'rate_limited' 视为瞬态错误"
        echo "  - 单候选者场景下强制使用该凭证"
        echo ""
        echo "修复方案："
        echo "  1. 修改 domains/streaming/executors/router.go"
        echo "  2. 在 isTransientUnavailableReason 函数中"
        echo "  3. 移除 'availability:rate_limited' 和 'state:rate_limit'"
        echo ""
        echo "详细分析请查看: $output_file"

    elif [ "$has_model_not_found" -gt 0 ] && [ "$degraded_usage" -gt 0 ]; then
        log_error "【问题确认】错误分类有问题 + 降级模式被触发！"
        echo ""
        echo "问题根因："
        echo "  - HTTP 429 被错误分类为 model_not_found"
        echo "  - 降级模式也在使用不可用的凭证"
        echo ""
        echo "修复方案："
        echo "  1. 修改 errorsx/classify.go"
        echo "  2. 确保 HTTP 429 优先返回 KindRateLimit"
        echo "  3. 同时修改降级模式逻辑"
        echo ""

    elif [ "$has_rate_limit" -gt 0 ] && [ "$degraded_usage" -eq 0 ]; then
        log_warning "【问题确认】自动恢复机制可能有问题"
        echo ""
        echo "问题根因："
        echo "  - 错误分类正确"
        echo "  - 降级模式没有被触发"
        echo "  - 可能是自动恢复后立即再次失败"
        echo ""
        echo "修复方案："
        echo "  1. 修改 credentialhealth/checker.go"
        echo "  2. 在 RecoverExpired 中添加探测验证"
        echo ""

    elif [ "$has_model_not_found" -gt 0 ]; then
        log_warning "【问题确认】可能是模型配置问题"
        echo ""
        echo "问题根因："
        echo "  - 出现 model_not_found 错误"
        echo "  - 可能是模型名称配置错误"
        echo ""
        echo "修复方案："
        echo "  1. 检查智谱AI使用的模型名"
        echo "  2. 确认是 glm-5.2 还是其他版本"
        echo "  3. 更新 provider_models 配置"
        echo ""

    else
        log_success "未检测到明显的错误模式"
        echo ""
        echo "可能的情况："
        echo "  1. 问题已经解决"
        echo "  2. 最近24小时没有失败请求"
        echo "  3. 需要更长时间窗口的数据"
        echo ""
        echo "建议："
        echo "  - 查看完整验证结果: $output_file"
        echo "  - 或者调整时间窗口（修改SQL中的 interval '24 hours'）"
    fi

    echo ""
    echo "完整验证结果已保存到: $output_file"
}

# 快速诊断（单独的SQL查询）
quick_diagnosis() {
    log_info "执行快速诊断..."
    echo ""

    # 快速检查1: 智谱AI错误类型
    echo "=== 快速检查1: 智谱AI错误类型 ==="
    psql "$DATABASE_URL" -c "
    SELECT error_kind, upstream_status_code, COUNT(*) as cnt
    FROM request_logs
    WHERE provider_code = 'zhipuai'
      AND request_status = 'failure'
      AND created_at > now() - interval '24 hours'
    GROUP BY error_kind, upstream_status_code
    ORDER BY cnt DESC
    LIMIT 5;
    " 2>&1 | tee /tmp/quick_check1.txt
    echo ""

    # 快速检查2: 降级模式证据
    echo "=== 快速检查2: 降级模式触发次数 ==="
    psql "$DATABASE_URL" -c "
    WITH unavailable_creds AS (
        SELECT credential_id, raw_model_name, unavailable_at
        FROM credential_model_bindings cmb
        JOIN provider_models pm ON pm.id = cmb.provider_model_id
        WHERE cmb.available = FALSE
          AND cmb.unavailable_at > now() - interval '1 hour'
    )
    SELECT COUNT(*) as degraded_usage_count
    FROM unavailable_creds uc
    JOIN request_logs rl ON rl.credential_id = uc.credential_id
    WHERE rl.created_at > uc.unavailable_at;
    " 2>&1 | tee /tmp/quick_check2.txt
    echo ""

    # 分析快速检查结果
    local degraded_count=$(grep -oE '[0-9]+' /tmp/quick_check2.txt | tail -1 || echo "0")

    if [ "$degraded_count" -gt 0 ]; then
        log_error "降级模式已触发 $degraded_count 次！"
        echo ""
        echo "这证明了降级模式正在强制使用不可用的凭证。"
    else
        log_success "降级模式未被触发"
    fi
}

# 主函数
main() {
    echo "================================================================"
    echo "         LLM Gateway 配置验证与问题诊断工具"
    echo "                   $(date '+%Y-%m-%d %H:%M:%S')"
    echo "================================================================"
    echo ""

    # 检查当前目录
    if [ ! -f "go.mod" ]; then
        log_error "请在项目根目录下运行此脚本"
        exit 1
    fi

    # 检查数据库连接
    check_database_connection
    echo ""

    # 提供选项
    echo "请选择操作："
    echo "  1. 完整验证（配置 + 验证 + 诊断）"
    echo "  2. 仅执行配置（添加火山引擎 GLM-5.2）"
    echo "  3. 仅执行验证"
    echo "  4. 快速诊断（2个关键SQL）"
    echo "  5. 退出"
    echo ""
    read -p "请输入选项 [1-5]: " choice

    case $choice in
        1)
            execute_volcano_config
            echo ""
            execute_verification
            ;;
        2)
            execute_volcano_config
            ;;
        3)
            execute_verification
            ;;
        4)
            quick_diagnosis
            ;;
        5)
            log_info "退出"
            exit 0
            ;;
        *)
            log_error "无效的选项"
            exit 1
            ;;
    esac

    echo ""
    echo "================================================================"
    log_success "执行完成！"
    echo "================================================================"
}

# 运行主函数
main "$@"
