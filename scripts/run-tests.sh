#!/usr/bin/env bash
# 综合测试启动器 - 提供交互式菜单和快捷命令

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${BLUE}ℹ ${NC}$*"
}

log_success() {
    echo -e "${GREEN}✓${NC} $*"
}

log_warning() {
    echo -e "${YELLOW}⚠${NC} $*"
}

log_error() {
    echo -e "${RED}✗${NC} $*"
}

# 检查必需工具
check_dependencies() {
    local missing=()
    
    if ! command -v maestro &>/dev/null; then
        missing+=("maestro")
    fi
    
    if ! command -v k6 &>/dev/null; then
        missing+=("k6")
    fi
    
    if [ ${#missing[@]} -gt 0 ]; then
        log_error "缺少必需工具: ${missing[*]}"
        echo ""
        echo "安装说明："
        echo "  Maestro: curl -fsSL https://get.mobile.dev | bash"
        echo "  k6:      brew install k6"
        return 1
    fi
    
    return 0
}

# 检查本地网关
check_gateway() {
    local url="${1:-http://127.0.0.1:8782}"
    
    log_info "检查网关连接: $url"
    if ! curl -sf "$url/health" &>/dev/null; then
        log_error "无法连接到网关: $url"
        log_warning "请确保网关正在运行"
        return 1
    fi
    
    log_success "网关运行正常"
    return 0
}

# 快速检查（仅Maestro UI测试）
run_quick_check() {
    log_info "执行快速检查（Maestro UI测试）..."
    
    local maestro_dir="$PROJECT_ROOT/maestro/flows"
    local timestamp=$(date +%Y%m%d-%H%M%S)
    local report_dir="$PROJECT_ROOT/test-reports/quick-$timestamp"
    
    mkdir -p "$report_dir"
    
    # 运行核心flows（跳过开发和主题）
    local flows=(
        "01-public-guest.yaml"
        "02-overview-live.yaml"
        "03-providers-models.yaml"
        "04-tenants-portal.yaml"
        "05-routing.yaml"
        "06-keys-requests.yaml"
    )
    
    local failed=0
    for flow in "${flows[@]}"; do
        log_info "运行: $flow"
        if maestro test "$maestro_dir/$flow" --format junit --output "$report_dir/${flow%.yaml}.xml" 2>&1 | tee "$report_dir/${flow%.yaml}.log"; then
            log_success "$flow 完成"
        else
            log_error "$flow 失败"
            ((failed++))
        fi
    done
    
    log_info "报告位置: $report_dir"
    
    if [ $failed -eq 0 ]; then
        log_success "快速检查完成：所有测试通过"
        return 0
    else
        log_error "快速检查完成：$failed 个测试失败"
        return 1
    fi
}

# 性能测试（仅k6）
run_performance_test() {
    log_info "执行性能测试（k6负载测试）..."
    
    local k6_dir="$PROJECT_ROOT/k6-scripts"
    local timestamp=$(date +%Y%m%d-%H%M%S)
    local report_dir="$PROJECT_ROOT/test-reports/perf-$timestamp"
    
    mkdir -p "$report_dir"
    
    # 运行k6测试
    local scripts=(
        "api-load-test.js"
        "streaming-load-test.js"
        "admin-load-test.js"
    )
    
    local failed=0
    for script in "${scripts[@]}"; do
        log_info "运行: $script"
        if k6 run "$k6_dir/$script" --out json="$report_dir/${script%.js}.json" 2>&1 | tee "$report_dir/${script%.js}.log"; then
            log_success "$script 完成"
        else
            log_error "$script 失败"
            ((failed++))
        fi
    done
    
    log_info "报告位置: $report_dir"
    
    if [ $failed -eq 0 ]; then
        log_success "性能测试完成：所有测试通过"
        return 0
    else
        log_error "性能测试完成：$failed 个测试失败"
        return 1
    fi
}

# 完整测试（Maestro + k6 并行）
run_full_test() {
    log_info "执行完整测试（并行运行）..."
    
    if [ -x "$SCRIPT_DIR/parallel-test-runner.sh" ]; then
        bash "$SCRIPT_DIR/parallel-test-runner.sh" local --with-k6
    else
        log_error "找不到 parallel-test-runner.sh"
        return 1
    fi
}

# 主题测试
run_theme_test() {
    log_info "执行主题测试（明暗皮肤验证）..."
    
    local maestro_dir="$PROJECT_ROOT/maestro/flows"
    local timestamp=$(date +%Y%m%d-%H%M%S)
    local report_dir="$PROJECT_ROOT/test-reports/theme-$timestamp"
    
    mkdir -p "$report_dir"
    
    if [ -f "$maestro_dir/98-theme-validation.yaml" ]; then
        maestro test "$maestro_dir/98-theme-validation.yaml" --format junit --output "$report_dir/theme-validation.xml" 2>&1 | tee "$report_dir/theme-validation.log"
        
        log_info "截图位置: $report_dir"
        log_success "主题测试完成"
    else
        log_error "找不到主题测试flow: 98-theme-validation.yaml"
        return 1
    fi
}

# 211环境测试
run_211_test() {
    log_info "执行211环境测试..."
    
    if ! check_gateway "http://10.211.55.4:8782"; then
        return 1
    fi
    
    if [ -x "$SCRIPT_DIR/parallel-test-runner.sh" ]; then
        bash "$SCRIPT_DIR/parallel-test-runner.sh" 211 --with-k6
    else
        log_error "找不到 parallel-test-runner.sh"
        return 1
    fi
}

# 交互式菜单
show_menu() {
    clear
    echo "╔════════════════════════════════════════════════════════════╗"
    echo "║         LLM Gateway 综合测试套件                          ║"
    echo "╚════════════════════════════════════════════════════════════╝"
    echo ""
    echo "请选择测试模式："
    echo ""
    echo "  1) 快速检查      - Maestro UI测试 (~10分钟)"
    echo "  2) 性能测试      - k6负载测试 (~25分钟)"
    echo "  3) 完整测试      - Maestro + k6 并行 (~30分钟)"
    echo "  4) 主题测试      - 明暗皮肤验证 (~5分钟)"
    echo "  5) 211环境测试   - 生产环境验证"
    echo ""
    echo "  8) 检查依赖"
    echo "  9) 查看帮助"
    echo "  0) 退出"
    echo ""
    echo -n "请输入选项 [0-9]: "
}

# 主函数
main() {
    # 命令行快捷方式
    if [ $# -gt 0 ]; then
        case "$1" in
            quick)
                check_dependencies || exit 1
                check_gateway || exit 1
                run_quick_check
                exit $?
                ;;
            perf|performance)
                check_dependencies || exit 1
                check_gateway || exit 1
                run_performance_test
                exit $?
                ;;
            full)
                check_dependencies || exit 1
                check_gateway || exit 1
                run_full_test
                exit $?
                ;;
            theme)
                check_dependencies || exit 1
                check_gateway || exit 1
                run_theme_test
                exit $?
                ;;
            211)
                check_dependencies || exit 1
                run_211_test
                exit $?
                ;;
            help|--help|-h)
                echo "用法: $0 [quick|perf|full|theme|211]"
                echo ""
                echo "快捷命令："
                echo "  quick  - 快速检查（仅Maestro）"
                echo "  perf   - 性能测试（仅k6）"
                echo "  full   - 完整测试（并行）"
                echo "  theme  - 主题测试"
                echo "  211    - 211环境测试"
                echo ""
                echo "无参数时显示交互式菜单"
                exit 0
                ;;
            *)
                log_error "未知命令: $1"
                echo "运行 '$0 help' 查看帮助"
                exit 1
                ;;
        esac
    fi
    
    # 交互式菜单循环
    while true; do
        show_menu
        read -r choice
        
        case "$choice" in
            1)
                check_dependencies || continue
                check_gateway || continue
                run_quick_check
                echo ""
                read -rp "按Enter键继续..."
                ;;
            2)
                check_dependencies || continue
                check_gateway || continue
                run_performance_test
                echo ""
                read -rp "按Enter键继续..."
                ;;
            3)
                check_dependencies || continue
                check_gateway || continue
                run_full_test
                echo ""
                read -rp "按Enter键继续..."
                ;;
            4)
                check_dependencies || continue
                check_gateway || continue
                run_theme_test
                echo ""
                read -rp "按Enter键继续..."
                ;;
            5)
                check_dependencies || continue
                run_211_test
                echo ""
                read -rp "按Enter键继续..."
                ;;
            8)
                check_dependencies
                check_gateway
                echo ""
                read -rp "按Enter键继续..."
                ;;
            9)
                cat "$SCRIPT_DIR/README-testing.md"
                echo ""
                read -rp "按Enter键继续..."
                ;;
            0)
                log_info "退出测试套件"
                exit 0
                ;;
            *)
                log_warning "无效选项，请重新选择"
                sleep 1
                ;;
        esac
    done
}

main "$@"
