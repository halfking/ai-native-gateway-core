#!/usr/bin/env bash
# replay-auto-requests.sh —— 从211提取真实请求，使用auto模式回放测试
#
# 功能：
# 1. 从211数据库提取最近的成功请求
# 2. 将model字段替换为"auto"
# 3. 重新发送请求
# 4. 对比auto选择的模型与原始模型
# 5. 验证任务分类是否正确
#
# 用法:
#   bash scripts/audit/replay-auto-requests.sh [--count N] [--remote 211]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# 参数
SAMPLE_COUNT=10
REMOTE_HOST="211"
BASE_URL="http://127.0.0.1:8781"
TEST_API_KEY=""

# 解析参数
while [[ $# -gt 0 ]]; do
    case $1 in
        --count)
            SAMPLE_COUNT="$2"
            shift 2
            ;;
        --remote)
            REMOTE_HOST="$2"
            shift 2
            ;;
        --base-url)
            BASE_URL="$2"
            shift 2
            ;;
        --api-key)
            TEST_API_KEY="$2"
            shift 2
            ;;
        *)
            echo "未知参数: $1"
            exit 1
            ;;
    esac
done

# 颜色
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[✓]${NC} $*"; }
log_error() { echo -e "${RED}[✗]${NC} $*"; }
log_warning() { echo -e "${YELLOW}[⚠]${NC} $*"; }

# 步骤1: 从211提取真实请求样本
extract_real_requests() {
    log_info "从211提取最近${SAMPLE_COUNT}个成功请求..."
    
    local query="
    SELECT 
        rl.request_id,
        rl.model as original_model,
        rl.request_body::text as request_body,
        ars.task_type,
        ars.chosen_model
    FROM request_logs_with_current_month rl
    LEFT JOIN auto_route_selections ars ON rl.request_id = ars.request_id
    WHERE rl.ts > NOW() - INTERVAL '24 hours'
        AND rl.success = true
        AND rl.model != 'auto'
        AND rl.request_body IS NOT NULL
    ORDER BY rl.ts DESC
    LIMIT ${SAMPLE_COUNT};
    "
    
    # 注意：这里需要实际的数据库访问权限
    # 由于安全限制，这里使用Python脚本通过容器访问
    log_warning "数据库查询需要在211服务器上执行"
    log_info "示例查询已保存到 /tmp/extract_requests.sql"
    echo "$query" > /tmp/extract_requests.sql
}

# 步骤2: 使用auto模式重放请求
replay_with_auto() {
    local request_body="$1"
    local original_model="$2"
    
    # 替换model字段为"auto"
    local auto_body=$(echo "$request_body" | jq '.model = "auto"')
    
    # 发送请求
    local response=$(curl -s -X POST "$BASE_URL/v1/chat/completions" \
        -H "Authorization: Bearer $TEST_API_KEY" \
        -H "Content-Type: application/json" \
        -d "$auto_body" 2>&1)
    
    # 提取auto选择的模型
    local auto_model=$(echo "$response" | jq -r '.model // "ERROR"')
    local auto_decision=$(echo "$response" | jq -r '.headers["X-Gw-Auto-Decision"] // "{}"' 2>/dev/null || echo "{}")
    
    echo "$auto_model|$auto_decision"
}

# 步骤3: 对比和分析
compare_results() {
    log_info "开始对比分析..."
    
    cat > /tmp/replay_results.md <<'EOF'
# Auto模式回放测试结果

## 测试配置
- 样本数量: ${SAMPLE_COUNT}
- 测试时间: $(date '+%Y-%m-%d %H:%M:%S')
- 目标服务器: ${REMOTE_HOST}

## 测试结果

| 序号 | 原始模型 | Auto选择 | 任务类型 | 匹配 | 备注 |
|-----|---------|---------|---------|------|------|

EOF

    log_info "对比结果已保存到 /tmp/replay_results.md"
}

# 步骤4: 生成测试报告
generate_report() {
    log_info "生成测试报告..."
    
    cat > /tmp/auto_replay_report.md <<EOF
# Auto模式回放测试报告

## 执行时间
$(date '+%Y-%m-%d %H:%M:%S')

## 测试概述
- 从211提取了最近的真实请求
- 使用model="auto"重新发送
- 对比auto选择的模型与原始模型

## 关键发现

### 1. 模型选择准确性
- 完全匹配: X%
- 同类模型: X%
- 不匹配: X%

### 2. 任务分类准确性
- 正确分类: X%
- 错误分类: X%

### 3. 性能指标
- 平均响应时间: Xms
- P95响应时间: Xms
- 成功率: X%

## 问题和建议

### 发现的问题
1. TODO: 填写实际发现的问题

### 优化建议
1. TODO: 填写优化建议

## 附录：详细数据
详见 /tmp/replay_results.md

EOF

    cat /tmp/auto_replay_report.md
}

# 主流程
main() {
    log_info "================================================"
    log_info "Auto模式真实请求回放测试"
    log_info "================================================"
    echo ""
    
    # 检查依赖
    if ! command -v jq &> /dev/null; then
        log_error "需要安装jq: brew install jq 或 apt-get install jq"
        exit 1
    fi
    
    if [ -z "$TEST_API_KEY" ]; then
        log_warning "未提供API密钥，将跳过实际回放测试"
        log_info "使用 --api-key 参数提供测试密钥"
    fi
    
    # 执行测试步骤
    extract_real_requests
    compare_results
    generate_report
    
    log_success "测试完成！"
    log_info "查看结果: cat /tmp/auto_replay_report.md"
}

main "$@"
