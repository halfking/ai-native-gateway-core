#!/bin/bash
# requires-bash: 4
# ↑ 本脚本第 203 行用了 mapfile/readarray（bash 4+ 特性）⇒ macOS 自带的
#   /bin/bash 是 **3.2**（无 mapfile）⇒ 运行前提是 bash ≥ 4。这条声明把该
#   前提从「隐含前提」变成**可检查的事实**
#   （sql/schema/shell_bash_prereq_declaration_gate_test.go）。
set -euo pipefail

# 并行测试执行器
# 用途：使用多个子代理并行执行测试，提高测试效率

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPORT_DIR="$PROJECT_ROOT/test-reports"

# 默认配置
MAX_PARALLEL=${MAX_PARALLEL:-4}  # 最大并行数
TIMEOUT=${TIMEOUT:-300}  # 单个测试超时（秒）

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_debug() {
    if [ "${DEBUG:-0}" = "1" ]; then
        echo -e "${BLUE}[DEBUG]${NC} $1"
    fi
}

# 执行单个测试任务
run_test_task() {
    local task_id=$1
    local test_file=$2
    local test_type=$3
    local log_file="$REPORT_DIR/parallel/task-${task_id}.log"
    
    mkdir -p "$REPORT_DIR/parallel"
    
    {
        echo "=========================================="
        echo "任务 ID: $task_id"
        echo "测试文件: $test_file"
        echo "测试类型: $test_type"
        echo "开始时间: $(date)"
        echo "=========================================="
        echo ""
    } > "$log_file"
    
    local start_time=$(date +%s)
    local exit_code=0
    
    case "$test_type" in
        maestro)
            timeout "$TIMEOUT" maestro test "$test_file" >> "$log_file" 2>&1 || exit_code=$?
            ;;
        k6)
            timeout "$TIMEOUT" k6 run --out json="$REPORT_DIR/k6/$(basename ${test_file%.js})-${task_id}-$(date +%Y%m%d-%H%M%S).json" "$test_file" >> "$log_file" 2>&1 || exit_code=$?
            ;;
        *)
            echo "未知测试类型: $test_type" >> "$log_file"
            exit_code=1
            ;;
    esac
    
    local end_time=$(date +%s)
    local duration=$((end_time - start_time))
    
    {
        echo ""
        echo "=========================================="
        echo "结束时间: $(date)"
        echo "执行时长: ${duration}秒"
        echo "退出码: $exit_code"
        echo "=========================================="
    } >> "$log_file"
    
    return $exit_code
}

# 并行执行测试
parallel_execute() {
    local test_files=("$@")
    local total=${#test_files[@]}
    
    log_info "开始并行执行 $total 个测试，最大并行度: $MAX_PARALLEL"
    
    local pids=()
    local task_id=0
    local completed=0
    local failed=0
    
    # 创建临时状态文件
    local status_dir="$REPORT_DIR/parallel/status"
    mkdir -p "$status_dir"
    rm -f "$status_dir"/*
    
    # 启动测试任务
    for test_file in "${test_files[@]}"; do
        # 等待有空闲槽位
        while [ $(jobs -r | wc -l) -ge $MAX_PARALLEL ]; do
            sleep 0.5
        done
        
        task_id=$((task_id + 1))
        
        # 确定测试类型
        local test_type=""
        if [[ "$test_file" == *.yaml ]]; then
            test_type="maestro"
        elif [[ "$test_file" == *.js ]]; then
            test_type="k6"
        else
            log_warn "跳过未知类型文件: $test_file"
            continue
        fi
        
        log_debug "启动任务 $task_id: $(basename $test_file)"
        
        # 后台执行
        (
            if run_test_task "$task_id" "$test_file" "$test_type"; then
                echo "0" > "$status_dir/$task_id"
            else
                echo "1" > "$status_dir/$task_id"
            fi
        ) &
        
        pids+=($!)
    done
    
    # 等待所有任务完成
    log_info "等待所有任务完成..."
    for pid in "${pids[@]}"; do
        wait "$pid" 2>/dev/null || true
        completed=$((completed + 1))
        
        # 显示进度
        local progress=$((completed * 100 / total))
        echo -ne "\r进度: $completed/$total ($progress%)  "
    done
    echo ""
    
    # 统计结果
    for status_file in "$status_dir"/*; do
        if [ -f "$status_file" ]; then
            local code=$(cat "$status_file")
            if [ "$code" != "0" ]; then
                failed=$((failed + 1))
            fi
        fi
    done
    
    log_info "=========================================="
    log_info "并行测试完成"
    log_info "总计: $total"
    log_info "完成: $completed"
    log_info "失败: $failed"
    log_info "成功率: $(( (total - failed) * 100 / total ))%"
    log_info "=========================================="
    
    return $failed
}

# 收集测试文件
collect_test_files() {
    local pattern=$1
    local files=()
    
    while IFS= read -r -d '' file; do
        files+=("$file")
    done < <(find "$PROJECT_ROOT/tests" -type f -name "$pattern" -print0 2>/dev/null)
    
    echo "${files[@]}"
}

# 主函数
main() {
    local test_type="${1:-all}"
    
    log_info "=========================================="
    log_info "并行测试执行器"
    log_info "最大并行度: $MAX_PARALLEL"
    log_info "单个测试超时: ${TIMEOUT}秒"
    log_info "=========================================="
    
    mkdir -p "$REPORT_DIR/parallel"
    
    local all_files=()
    
    case "$test_type" in
        maestro)
            log_info "收集 Maestro 测试文件..."
            mapfile -t all_files < <(find "$PROJECT_ROOT/tests/maestro/flows" -type f -name "*.yaml" 2>/dev/null || true)
            ;;
        k6)
            log_info "收集 k6 测试文件..."
            mapfile -t all_files < <(find "$PROJECT_ROOT/tests/k6" -type f -name "*.js" 2>/dev/null || true)
            ;;
        all)
            log_info "收集所有测试文件..."
            mapfile -t maestro_files < <(find "$PROJECT_ROOT/tests/maestro/flows" -type f -name "*.yaml" 2>/dev/null || true)
            mapfile -t k6_files < <(find "$PROJECT_ROOT/tests/k6" -type f -name "*.js" 2>/dev/null || true)
            all_files=("${maestro_files[@]}" "${k6_files[@]}")
            ;;
        *)
            log_error "未知测试类型: $test_type"
            echo "用法: $0 [maestro|k6|all]"
            exit 1
            ;;
    esac
    
    if [ ${#all_files[@]} -eq 0 ]; then
        log_warn "未找到测试文件"
        exit 0
    fi
    
    log_info "找到 ${#all_files[@]} 个测试文件"
    
    # 并行执行
    if parallel_execute "${all_files[@]}"; then
        log_info "所有测试通过 ✓"
        exit 0
    else
        log_error "部分测试失败，请查看日志"
        exit 1
    fi
}

# 执行主函数
main "$@"
