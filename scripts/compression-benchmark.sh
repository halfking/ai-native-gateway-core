#!/bin/bash
# scripts/compression-benchmark.sh
# 
# 压缩算法 benchmark 工具 - 使用 245 生产日志数据验证压缩效果
#
# 用法:
#   ./scripts/compression-benchmark.sh --data-source /path/to/245-logs --sample-size 1000
#
# 作者: ZCode Agent
# 日期: 2026-08-28

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认参数
DATA_SOURCE=""
SAMPLE_SIZE=1000
STRATEGIES="intelligent,tool-focused,hybrid"
OUTPUT="$PROJECT_ROOT/docs/benchmark/compression-results-$(date +%Y%m%d-%H%M%S).json"
EVALUATOR_MODEL="gpt-4o-mini"  # 便宜的评估器模型
DRY_RUN=""
VERBOSE=""

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

usage() {
    cat <<EOF
${BLUE}压缩算法 Benchmark 工具${NC}

${GREEN}用法:${NC}
    $0 [OPTIONS]

${GREEN}必需参数:${NC}
    --data-source PATH    245 生产日志数据路径

${GREEN}可选参数:${NC}
    --sample-size N       采样会话数量 (默认: 1000)
    --strategies CSV      逗号分隔的策略列表 (默认: intelligent,tool-focused,hybrid)
    --output PATH         输出 JSON 文件路径 (默认: docs/benchmark/...)
    --evaluator MODEL     LLM 评估器模型 (默认: gpt-4o-mini)
    --dry-run             仅显示采样分布，不执行压缩
    --verbose             详细输出
    --help                显示此帮助信息

${GREEN}策略选项:${NC}
    intelligent           当前 v4 intelligent compression (LLM 摘要)
    tool-focused          工具结果压缩优先 (学习 OmniRoute)
    rule-based            基于规则的文本压缩 (未实现)
    hybrid                intelligent + tool-focused 组合

${GREEN}示例:${NC}
    # 基础用法
    $0 --data-source /data/245-logs

    # 自定义采样和策略
    $0 --data-source /data/245-logs --sample-size 500 --strategies intelligent,hybrid

    # Dry-run 查看数据分布
    $0 --data-source /data/245-logs --dry-run

    # 详细输出
    $0 --data-source /data/245-logs --verbose

${GREEN}输出格式:${NC}
    JSON 文件包含:
    - 每个策略的平均压缩比、节省率、延迟
    - 信息密度和遗失率评估
    - 按会话类型分组的统计
    - P50/P95/P99 延迟分布

${GREEN}数据源格式:${NC}
    期望 245 日志为 JSONL 格式，每行一个会话:
    {
      "session_id": "xxx",
      "messages": [...],
      "estimated_tokens": 50000,
      "tool_call_count": 10
    }

EOF
}

# 参数解析
while [[ $# -gt 0 ]]; do
    case $1 in
        --data-source)
            DATA_SOURCE="$2"
            shift 2
            ;;
        --sample-size)
            SAMPLE_SIZE="$2"
            shift 2
            ;;
        --strategies)
            STRATEGIES="$2"
            shift 2
            ;;
        --output)
            OUTPUT="$2"
            shift 2
            ;;
        --evaluator)
            EVALUATOR_MODEL="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN="1"
            shift
            ;;
        --verbose)
            VERBOSE="1"
            shift
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            log_error "未知选项: $1"
            usage
            exit 1
            ;;
    esac
done

# 验证必需参数
if [[ -z "$DATA_SOURCE" ]]; then
    log_error "--data-source 是必需参数"
    echo ""
    usage
    exit 1
fi

if [[ ! -d "$DATA_SOURCE" ]] && [[ ! -f "$DATA_SOURCE" ]]; then
    log_error "数据源不存在: $DATA_SOURCE"
    exit 1
fi

# 创建输出目录
OUTPUT_DIR="$(dirname "$OUTPUT")"
mkdir -p "$OUTPUT_DIR"

# 检查 Go 环境
if ! command -v go &> /dev/null; then
    log_error "未找到 Go 环境，请先安装 Go"
    exit 1
fi

# 检查 compression-benchmark 工具是否需要构建
BENCHMARK_BIN="$PROJECT_ROOT/bin/compression-benchmark"
if [[ ! -f "$BENCHMARK_BIN" ]] || [[ "$PROJECT_ROOT/cmd/compression-benchmark/main.go" -nt "$BENCHMARK_BIN" ]]; then
    log_info "构建 compression-benchmark 工具..."
    mkdir -p "$PROJECT_ROOT/bin"
    if [[ -n "$VERBOSE" ]]; then
        go build -o "$BENCHMARK_BIN" "$PROJECT_ROOT/cmd/compression-benchmark/main.go"
    else
        go build -o "$BENCHMARK_BIN" "$PROJECT_ROOT/cmd/compression-benchmark/main.go" 2>&1 | grep -v "^#" || true
    fi
    log_success "构建完成: $BENCHMARK_BIN"
fi

# 构建命令行参数
ARGS=(
    "--data-source" "$DATA_SOURCE"
    "--sample-size" "$SAMPLE_SIZE"
    "--strategies" "$STRATEGIES"
    "--evaluator" "$EVALUATOR_MODEL"
    "--output" "$OUTPUT"
)

if [[ -n "$DRY_RUN" ]]; then
    ARGS+=("--dry-run")
fi

if [[ -n "$VERBOSE" ]]; then
    ARGS+=("--verbose")
fi

# 显示配置
log_info "==============================================="
log_info "压缩算法 Benchmark"
log_info "==============================================="
log_info "数据源:        $DATA_SOURCE"
log_info "采样大小:      $SAMPLE_SIZE"
log_info "策略:          $STRATEGIES"
log_info "评估器模型:    $EVALUATOR_MODEL"
log_info "输出文件:      $OUTPUT"
if [[ -n "$DRY_RUN" ]]; then
    log_warn "Dry-run 模式 (不执行压缩)"
fi
log_info "==============================================="
echo ""

# 执行 benchmark
log_info "启动 benchmark..."
START_TIME=$(date +%s)

if [[ -n "$VERBOSE" ]]; then
    "$BENCHMARK_BIN" "${ARGS[@]}"
else
    "$BENCHMARK_BIN" "${ARGS[@]}" 2>&1 | while IFS= read -r line; do
        # 过滤掉 Go 内部日志，只显示关键信息
        if [[ "$line" =~ ^(INFO|WARN|ERROR|SUCCESS) ]] || [[ "$line" =~ ^[[:space:]]*$ ]]; then
            echo "$line"
        fi
    done
fi

EXIT_CODE=$?
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

echo ""
if [[ $EXIT_CODE -eq 0 ]]; then
    log_success "Benchmark 完成 (耗时: ${DURATION}s)"
    
    if [[ -z "$DRY_RUN" ]]; then
        log_success "结果已保存到: $OUTPUT"
        echo ""
        log_info "==============================================="
        log_info "结果摘要"
        log_info "==============================================="
        
        # 使用 jq 美化输出
        if command -v jq &> /dev/null; then
            echo ""
            jq -r '
                .results | to_entries | .[] | 
                "[\(.key)]",
                "  平均压缩比:     \(.value.avg_compression_ratio // "N/A")x",
                "  平均节省率:     \(.value.avg_savings_percent // "N/A")%",
                "  平均信息丢失:   \(.value.avg_information_loss // "N/A")%",
                "  P50 延迟:       \(.value.p50_latency_ms // "N/A")ms",
                "  P95 延迟:       \(.value.p95_latency_ms // "N/A")ms",
                "  P99 延迟:       \(.value.p99_latency_ms // "N/A")ms",
                ""
            ' "$OUTPUT"
        else
            log_warn "未安装 jq，无法美化输出。请手动查看: $OUTPUT"
        fi
        
        echo ""
        log_info "详细报告: $OUTPUT"
        log_info "可视化图表: ${OUTPUT%.json}.html (如果生成)"
    fi
else
    log_error "Benchmark 失败 (退出码: $EXIT_CODE)"
    exit $EXIT_CODE
fi

exit 0
