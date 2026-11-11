#!/bin/bash
set -e

# LLM Gateway 完整测试执行脚本
# 包含：Maestro UI测试、k6性能测试、主题验证

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TESTS_DIR="$PROJECT_ROOT/tests"
REPORTS_DIR="$TESTS_DIR/reports"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

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

# 创建报告目录
mkdir -p "$REPORTS_DIR/$TIMESTAMP"
REPORT_DIR="$REPORTS_DIR/$TIMESTAMP"

# 环境变量
export APP_URL="${APP_URL:-http://localhost:8080}"
export ADMIN_USER="${ADMIN_USER:-admin}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin123}"

log_info "=========================================="
log_info "LLM Gateway 完整测试套件"
log_info "时间: $(date)"
log_info "应用URL: $APP_URL"
log_info "报告目录: $REPORT_DIR"
log_info "=========================================="

# 1. 检查应用是否运行
log_info "检查应用健康状态..."
if curl -s -f "$APP_URL/" > /dev/null 2>&1; then
    log_success "应用运行正常"
else
    log_error "应用未运行或无法访问: $APP_URL"
    exit 1
fi

# 2. 运行Maestro UI测试
log_info "=========================================="
log_info "执行 Maestro UI 自动化测试..."
log_info "=========================================="

MAESTRO_DIR="$TESTS_DIR/maestro/flows"
MAESTRO_REPORT="$REPORT_DIR/maestro_results.txt"

if [ -d "$MAESTRO_DIR" ]; then
    PASSED=0
    FAILED=0
    TOTAL=0
    
    echo "Maestro UI 测试报告" > "$MAESTRO_REPORT"
    echo "==================" >> "$MAESTRO_REPORT"
    echo "执行时间: $(date)" >> "$MAESTRO_REPORT"
    echo "" >> "$MAESTRO_REPORT"
    
    for flow in "$MAESTRO_DIR"/*.yaml; do
        if [ -f "$flow" ]; then
            TOTAL=$((TOTAL + 1))
            FLOW_NAME=$(basename "$flow")
            log_info "执行测试: $FLOW_NAME"
            
            if maestro test "$flow" --format junit --output "$REPORT_DIR/maestro_${FLOW_NAME%.yaml}.xml" 2>&1 | tee -a "$MAESTRO_REPORT"; then
                PASSED=$((PASSED + 1))
                log_success "✓ $FLOW_NAME 通过"
                echo "✓ PASSED: $FLOW_NAME" >> "$MAESTRO_REPORT"
            else
                FAILED=$((FAILED + 1))
                log_error "✗ $FLOW_NAME 失败"
                echo "✗ FAILED: $FLOW_NAME" >> "$MAESTRO_REPORT"
            fi
            echo "" >> "$MAESTRO_REPORT"
        fi
    done
    
    echo "" >> "$MAESTRO_REPORT"
    echo "测试统计:" >> "$MAESTRO_REPORT"
    echo "总计: $TOTAL" >> "$MAESTRO_REPORT"
    echo "通过: $PASSED" >> "$MAESTRO_REPORT"
    echo "失败: $FAILED" >> "$MAESTRO_REPORT"
    
    log_info "Maestro测试完成: $PASSED/$TOTAL 通过"
else
    log_warning "未找到Maestro测试流程"
fi

# 3. 运行k6性能测试
log_info "=========================================="
log_info "执行 k6 性能测试..."
log_info "=========================================="

K6_DIR="$TESTS_DIR/k6/scenarios"
K6_REPORT="$REPORT_DIR/k6_results.txt"

if [ -d "$K6_DIR" ]; then
    echo "k6 性能测试报告" > "$K6_REPORT"
    echo "==============" >> "$K6_REPORT"
    echo "执行时间: $(date)" >> "$K6_REPORT"
    echo "" >> "$K6_REPORT"
    
    for script in "$K6_DIR"/*.js; do
        if [ -f "$script" ]; then
            SCRIPT_NAME=$(basename "$script")
            log_info "执行性能测试: $SCRIPT_NAME"
            
            k6 run --out json="$REPORT_DIR/k6_${SCRIPT_NAME%.js}.json" "$script" 2>&1 | tee -a "$K6_REPORT"
            
            if [ ${PIPESTATUS[0]} -eq 0 ]; then
                log_success "✓ $SCRIPT_NAME 完成"
            else
                log_warning "⚠ $SCRIPT_NAME 有警告"
            fi
            echo "" >> "$K6_REPORT"
        fi
    done
else
    log_warning "未找到k6性能测试脚本"
fi

# 4. 生成测试总结
log_info "=========================================="
log_info "生成测试总结报告..."
log_info "=========================================="

SUMMARY_REPORT="$REPORT_DIR/test-summary.md"

cat > "$SUMMARY_REPORT" << EOF
# LLM Gateway 测试报告

**执行时间**: $(date)
**应用URL**: $APP_URL
**测试环境**: 本地Docker

## 测试概览

### Maestro UI测试
- 测试流程数: $TOTAL
- 通过: $PASSED
- 失败: $FAILED
- 通过率: $(echo "scale=2; $PASSED * 100 / $TOTAL" | bc)%

### k6性能测试
详见 k6_results.txt

## 测试文件
- Maestro结果: \`maestro_results.txt\`
- k6结果: \`k6_results.txt\`
- 截图: \`screenshots/\`

## 下一步
EOF

if [ $FAILED -gt 0 ]; then
    echo "- ⚠️ 修复 $FAILED 个失败的测试" >> "$SUMMARY_REPORT"
fi

echo "- 部署到211环境进行生产测试" >> "$SUMMARY_REPORT"

log_success "测试报告已生成: $SUMMARY_REPORT"

# 5. 显示报告位置
log_info "=========================================="
log_info "测试完成！"
log_info "=========================================="
log_info "报告位置: $REPORT_DIR"
log_info "查看总结: cat $SUMMARY_REPORT"
log_info "查看Maestro结果: cat $MAESTRO_REPORT"
log_info "查看k6结果: cat $K6_REPORT"

# 返回状态码
if [ $FAILED -gt 0 ]; then
    log_warning "部分测试失败，请查看报告"
    exit 1
else
    log_success "所有测试通过！"
    exit 0
fi
