#!/bin/bash
set -euo pipefail

# 测试运行主脚本
# 用途：协调所有测试的执行，包括UI自动化测试和性能测试

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPORT_DIR="$PROJECT_ROOT/test-reports"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_service() {
    local port=$1
    local max_attempts=30
    local attempt=1
    
    echo "等待端口 $port 服务启动..."
    while [ $attempt -le $max_attempts ]; do
        # 使用无需认证的版本端点检查服务状态
        if curl -sf "http://localhost:$port/api/system/version" > /dev/null 2>&1; then
            echo "✓ 端口 $port 服务已就绪"
            return 0
        fi
        echo "  尝试 $attempt/$max_attempts..."
        sleep 2
        attempt=$((attempt + 1))
    done
    
    echo "✗ 端口 $port 服务启动超时"
    return 1
}

# 检查前置条件
check_prerequisites() {
    log_info "检查测试前置条件..."
    
    # 检查 Maestro
    if ! command -v maestro &> /dev/null; then
        log_error "Maestro 未安装，请运行: curl -Ls 'https://get.maestro.mobile.dev' | bash"
        return 1
    fi
    
    # 检查 k6
    if ! command -v k6 &> /dev/null; then
        log_error "k6 未安装，请运行: brew install k6 (macOS) 或访问 https://k6.io/docs/getting-started/installation/"
        return 1
    fi
    
    # 检查服务是否运行
    if ! check_service 8782; then
        log_error "本地网关服务未运行，请先启动服务"
        return 1
    fi
    
    log_info "所有前置条件满足 ✓"
    return 0
}

# 创建报告目录
prepare_report_dir() {
    log_info "准备测试报告目录..."
    mkdir -p "$REPORT_DIR"/{maestro,k6,screenshots}
    log_info "报告目录: $REPORT_DIR"
}

# 运行 Maestro UI 测试
run_maestro_tests() {
    log_info "=========================================="
    log_info "运行 Maestro UI 自动化测试"
    log_info "=========================================="
    
    local flows_dir="$PROJECT_ROOT/tests/maestro/flows"
    local maestro_report="$REPORT_DIR/maestro/maestro-report-$(date +%Y%m%d-%H%M%S).txt"
    
    if [ ! -d "$flows_dir" ]; then
        log_error "Maestro flows 目录不存在: $flows_dir"
        return 1
    fi
    
    {
        echo "Maestro UI 自动化测试报告"
        echo "时间: $(date)"
        echo "=========================================="
        echo ""
    } > "$maestro_report"
    
    local total=0
    local passed=0
    local failed=0
    
    for flow in "$flows_dir"/*.yaml; do
        if [ -f "$flow" ]; then
            total=$((total + 1))
            local flow_name=$(basename "$flow")
            log_info "运行测试: $flow_name"
            
            if maestro test "$flow" >> "$maestro_report" 2>&1; then
                passed=$((passed + 1))
                log_info "✓ $flow_name 通过"
            else
                failed=$((failed + 1))
                log_error "✗ $flow_name 失败"
            fi
            echo "" >> "$maestro_report"
        fi
    done
    
    {
        echo ""
        echo "=========================================="
        echo "Maestro 测试汇总:"
        echo "总计: $total"
        echo "通过: $passed"
        echo "失败: $failed"
        echo "=========================================="
    } >> "$maestro_report"
    
    log_info "Maestro 测试完成 - 通过: $passed/$total"
    log_info "详细报告: $maestro_report"
    
    return $failed
}

# 运行 k6 性能测试
run_k6_tests() {
    log_info "=========================================="
    log_info "运行 k6 性能测试"
    log_info "=========================================="
    
    local k6_dir="$PROJECT_ROOT/tests/k6"
    local k6_report="$REPORT_DIR/k6/k6-report-$(date +%Y%m%d-%H%M%S).json"
    
    if [ ! -d "$k6_dir" ]; then
        log_error "k6 测试目录不存在: $k6_dir"
        return 1
    fi
    
    local failed=0
    
    for script in "$k6_dir"/*.js; do
        if [ -f "$script" ]; then
            local script_name=$(basename "$script")
            log_info "运行性能测试: $script_name"
            
            if k6 run --out json="$REPORT_DIR/k6/${script_name%.js}-$(date +%Y%m%d-%H%M%S).json" "$script"; then
                log_info "✓ $script_name 完成"
            else
                failed=$((failed + 1))
                log_error "✗ $script_name 失败"
            fi
        fi
    done
    
    log_info "k6 测试完成"
    log_info "详细报告: $REPORT_DIR/k6/"
    
    return $failed
}

# 生成测试总结
generate_summary() {
    log_info "=========================================="
    log_info "生成测试总结"
    log_info "=========================================="
    
    local summary_file="$REPORT_DIR/test-summary-$(date +%Y%m%d-%H%M%S).json"
    
    cat > "$summary_file" <<EOF
{
  "test_run": {
    "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "environment": "local",
    "gateway_url": "http://localhost:8782"
  },
  "reports": {
    "maestro": "$REPORT_DIR/maestro/",
    "k6": "$REPORT_DIR/k6/",
    "screenshots": "$REPORT_DIR/screenshots/"
  }
}
EOF
    
    log_info "测试总结已保存: $summary_file"
}

# 主函数
main() {
    log_info "=========================================="
    log_info "LLM Gateway 测试套件"
    log_info "=========================================="
    
    # 检查前置条件
    if ! check_prerequisites; then
        exit 1
    fi
    
    # 准备报告目录
    prepare_report_dir
    
    local exit_code=0
    
    # 运行 Maestro 测试
    if ! run_maestro_tests; then
        exit_code=1
    fi
    
    # 运行 k6 测试
    if ! run_k6_tests; then
        exit_code=1
    fi
    
    # 生成总结
    generate_summary
    
    log_info "=========================================="
    if [ $exit_code -eq 0 ]; then
        log_info "所有测试完成 ✓"
    else
        log_error "部分测试失败，请查看报告"
    fi
    log_info "=========================================="
    
    exit $exit_code
}

# 执行主函数
main "$@"
