#!/usr/bin/env bash
# 性能基线测试执行脚本
# 用途：一键运行完整的性能基线测试套件
# 作者：ZCode AI
# 日期：2026-11-18

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
RESULTS_DIR="$ROOT_DIR/baseline-test-results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

# 创建结果目录
setup_results_dir() {
    log_info "创建结果目录..."
    mkdir -p "$RESULTS_DIR"/{raw-data,processed-data,summary,charts}
    log_success "结果目录创建完成: $RESULTS_DIR"
}

# 检查环境
check_environment() {
    log_info "检查测试环境..."
    
    local missing=()
    
    # 检查必需工具
    command -v python3 >/dev/null 2>&1 || missing+=("python3")
    command -v psql >/dev/null 2>&1 || missing+=("psql")
    command -v curl >/dev/null 2>&1 || missing+=("curl")
    command -v jq >/dev/null 2>&1 || missing+=("jq")
    command -v go >/dev/null 2>&1 || missing+=("go")
    
    if [[ ${#missing[@]} -gt 0 ]]; then
        log_error "缺少依赖: ${missing[*]}"
        log_error "请安装缺失的工具后重试"
        exit 1
    fi
    
    # 检查数据库连接
    if ! psql -h "${DB_HOST:-localhost}" -p "${DB_PORT:-5432}" -U "${DB_USER:-xutaohuang}" -d "${DB_NAME:-llm_gateway}" -c "SELECT 1" >/dev/null 2>&1; then
        log_error "无法连接到数据库"
        log_error "请检查数据库配置和运行状态"
        exit 1
    fi
    
    log_success "环境检查通过"
}

# 测试1：正常负载测试
run_normal_load_test() {
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_info "测试1: 正常负载测试（150并发 × 80轮）"
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    
    local log_file="$RESULTS_DIR/raw-data/test-1-normal-load-$TIMESTAMP.log"
    
    log_info "日志文件: $log_file"
    log_info "预计耗时: 15-30分钟"
    log_info "开始时间: $(date)"
    
    if [ -x "$SCRIPT_DIR/comprehensive-loadtest.sh" ]; then
        "$SCRIPT_DIR/comprehensive-loadtest.sh" 2>&1 | tee "$log_file"
        log_success "测试1完成"
    else
        log_error "找不到comprehensive-loadtest.sh或无执行权限"
        return 1
    fi
    
    log_info "结束时间: $(date)"
}

# 测试2：渐进压力测试（如果脚本存在）
run_progressive_test() {
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_info "测试2: 渐进压力测试（可选）"
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    
    if [ -x "$SCRIPT_DIR/progressive-load-test.sh" ]; then
        local log_file="$RESULTS_DIR/raw-data/test-2-progressive-$TIMESTAMP.log"
        log_info "日志文件: $log_file"
        
        "$SCRIPT_DIR/progressive-load-test.sh" 2>&1 | tee "$log_file"
        log_success "测试2完成"
    else
        log_warn "progressive-load-test.sh不存在，跳过渐进测试"
        log_warn "（这是可选测试，不影响基线建立）"
    fi
}

# 测试3：缓存效率测试（如果脚本存在）
run_cache_test() {
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_info "测试3: 缓存效率测试（可选）"
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    
    if [ -x "$SCRIPT_DIR/cache-efficiency-test.sh" ]; then
        local log_file="$RESULTS_DIR/raw-data/test-3-cache-$TIMESTAMP.log"
        log_info "日志文件: $log_file"
        
        export CACHE_ENABLED=true
        "$SCRIPT_DIR/cache-efficiency-test.sh" 2>&1 | tee "$log_file"
        log_success "测试3完成"
    else
        log_warn "cache-efficiency-test.sh不存在，跳过缓存测试"
        log_warn "（这是可选测试，不影响基线建立）"
    fi
}

# 测试4：重复验证
run_repeat_test() {
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_info "测试4: 重复验证（验证数据一致性）"
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    
    local log_file="$RESULTS_DIR/raw-data/test-4-repeat-$TIMESTAMP.log"
    
    log_info "日志文件: $log_file"
    log_info "这是对测试1的重复运行，用于验证结果一致性"
    log_info "开始时间: $(date)"
    
    if [ -x "$SCRIPT_DIR/comprehensive-loadtest.sh" ]; then
        "$SCRIPT_DIR/comprehensive-loadtest.sh" 2>&1 | tee "$log_file"
        log_success "测试4完成"
    else
        log_error "找不到comprehensive-loadtest.sh"
        return 1
    fi
    
    log_info "结束时间: $(date)"
}

# 生成分析报告
generate_analysis() {
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    log_info "生成分析报告..."
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    
    # 创建汇总文件
    local summary_file="$RESULTS_DIR/summary/baseline-summary-$TIMESTAMP.txt"
    
    cat > "$summary_file" << EOF
═══════════════════════════════════════════════════════════════
  LLM Gateway 性能基线测试汇总
═══════════════════════════════════════════════════════════════

测试时间: $TIMESTAMP
测试环境:
- 主机: $(hostname)
- OS: $(uname -s) $(uname -r)
- Go版本: $(go version)
- CPU: $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo "未知")
- 内存: $(sysctl -n hw.memsize 2>/dev/null | awk '{print $1/1024/1024/1024 " GB"}' || echo "未知")

测试配置:
- 并发客户端: 150
- 每客户端轮次: 80
- 总请求数: 12000
- Mock Providers: 20

测试结果:
（请查看原始日志文件以获取详细数据）

原始数据位置:
$RESULTS_DIR/raw-data/

分析脚本:
如需详细分析，请运行:
./scripts/baseline-analysis.sh

下一步:
1. 查看测试日志，提取关键指标
2. 填写performance-baseline-report.md
3. 对比P0-14目标，确定优化重点
4. 开始执行P0-14优化工作

═══════════════════════════════════════════════════════════════
EOF
    
    log_success "汇总报告已生成: $summary_file"
    
    # 显示文件位置
    log_info ""
    log_info "所有测试数据已保存到:"
    log_info "  $RESULTS_DIR"
    log_info ""
    log_info "查看汇总:"
    log_info "  cat $summary_file"
}

# 主函数
main() {
    log_info "═══════════════════════════════════════════════════════════════"
    log_info "  LLM Gateway 性能基线测试套件"
    log_info "  版本: v1.0"
    log_info "  时间: $(date)"
    log_info "═══════════════════════════════════════════════════════════════"
    log_info ""
    
    # Step 1: 环境准备
    setup_results_dir
    check_environment
    
    log_info ""
    log_info "准备开始测试..."
    log_info "预计总耗时: 1-2小时（取决于系统性能）"
    log_info ""
    
    # 询问用户确认
    read -p "是否继续？(y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log_warn "用户取消测试"
        exit 0
    fi
    
    log_info ""
    
    # Step 2: 执行测试套件
    local test_start_time=$(date +%s)
    
    run_normal_load_test || {
        log_error "测试1失败，终止测试"
        exit 1
    }
    
    # 可选测试（失败不影响整体）
    run_progressive_test || log_warn "测试2失败或跳过"
    run_cache_test || log_warn "测试3失败或跳过"
    
    run_repeat_test || {
        log_warn "测试4（重复验证）失败"
        log_warn "建议手动重新运行以验证数据一致性"
    }
    
    local test_end_time=$(date +%s)
    local test_duration=$((test_end_time - test_start_time))
    
    log_info ""
    log_info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    
    # Step 3: 生成报告
    generate_analysis
    
    log_info ""
    log_success "═══════════════════════════════════════════════════════════════"
    log_success "  所有测试完成！"
    log_success "  总耗时: $((test_duration / 60)) 分钟 $((test_duration % 60)) 秒"
    log_success "═══════════════════════════════════════════════════════════════"
    log_info ""
    log_info "下一步："
    log_info "1. 查看测试日志提取关键指标"
    log_info "2. 填写 docs/performance-baseline-report.md"
    log_info "3. 运行 ./scripts/baseline-analysis.sh 进行深度分析"
    log_info "4. 对比P0-14目标，制定优化计划"
    log_info ""
}

# 执行主函数
main "$@"
