#!/bin/bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# analytics_e2e_contract_test.sh — 分析详情系统端到端契约测试 (2026-11-20)
#
# 验证前后端接口契约的一致性:
# 1. 前端API调用函数签名
# 2. 后端API端点路由
# 3. 数据结构对应关系
# 4. 编译验证

set -e

echo "=========================================="
echo "分析详情系统 E2E 契约测试"
echo "=========================================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

PASSED=0
FAILED=0

# 测试函数
test_case() {
    local name="$1"
    local cmd="$2"
    
    echo -n "测试: $name ... "
    
    if eval "$cmd" > /dev/null 2>&1; then
        echo -e "${GREEN}✓ PASS${NC}"
        ((PASSED++))
        return 0
    else
        echo -e "${RED}✗ FAIL${NC}"
        ((FAILED++))
        return 1
    fi
}

echo "1. 前端契约验证"
echo "----------------------------------------"

# 1.1 检查前端文件存在性
test_case "前端注册表文件存在" \
    "test -f web/src/config/analyticsRegistry.ts"

test_case "前端API类型定义存在" \
    "test -f web/src/api/board.ts"

test_case "列表页组件存在" \
    "test -f web/src/views/BoardOperationalListView.vue"

test_case "详情页组件存在" \
    "test -f web/src/views/analytics/AnalyticsDetailView.vue"

# 1.2 检查API函数定义
test_case "fetchAnalyticsDetail函数已定义" \
    "grep -q 'export function fetchAnalyticsDetail' web/src/api/board.ts"

# 1.3 检查类型定义
test_case "AnalyticsDetailPayload类型已定义" \
    "grep -q 'export interface AnalyticsDetailPayload' web/src/api/board.ts"

test_case "AnalyticsSummary类型已定义" \
    "grep -q 'export interface AnalyticsSummary' web/src/api/board.ts"

test_case "AnalyticsTrendPoint类型已定义" \
    "grep -q 'export interface AnalyticsTrendPoint' web/src/api/board.ts"

# 1.4 检查路由配置
test_case "前端路由已注册(列表页)" \
    "grep -q '/admin/board-operational/:dimension' web/src/router.ts"

test_case "前端路由已注册(详情页)" \
    "grep -q '/admin/analytics/:dimension/:id' web/src/router.ts"

echo ""
echo "2. 后端契约验证"
echo "----------------------------------------"

# 2.1 检查后端文件存在性
test_case "后端处理器文件存在" \
    "test -f admin/analytics_handlers.go"

test_case "后端查询文件存在" \
    "test -f admin/analytics_queries.go"

test_case "后端测试文件存在" \
    "test -f admin/analytics_handlers_test.go"

# 2.2 检查处理器函数
test_case "handleAnalyticsDetail函数已定义" \
    "grep -q 'func.*handleAnalyticsDetail' admin/analytics_handlers.go"

# 2.3 检查数据结构
test_case "AnalyticsDetailPayload结构体已定义" \
    "grep -q 'type AnalyticsDetailPayload struct' admin/analytics_handlers.go"

test_case "AnalyticsSummary结构体已定义" \
    "grep -q 'type AnalyticsSummary struct' admin/analytics_handlers.go"

test_case "AnalyticsTrendPoint结构体已定义" \
    "grep -q 'type AnalyticsTrendPoint struct' admin/analytics_handlers.go"

# 2.4 检查路由注册
test_case "后端路由已注册" \
    "grep -q '/api/admin/analytics/{dimension}/{identifier}' admin/handler.go"

# 2.5 检查查询函数
test_case "汇总查询函数已定义" \
    "grep -q 'func.*queryAnalyticsSummary' admin/analytics_queries.go"

test_case "趋势查询函数已定义" \
    "grep -q 'func.*queryAnalyticsTrends' admin/analytics_queries.go"

test_case "交叉分析查询函数已定义" \
    "grep -q 'func.*queryAnalyticsCrossAnalysis' admin/analytics_queries.go"

echo ""
echo "3. 前后端契约一致性验证"
echo "----------------------------------------"

# 3.1 API路径一致性
test_case "API路径格式一致" \
    "grep -q '/api/admin/analytics/\${dimension}/\${encodeURIComponent(identifier)}' web/src/api/board.ts"

# 3.2 维度值一致性
test_case "前端支持4个维度" \
    "grep -A5 'type AnalyticsDimension' web/src/config/analyticsRegistry.ts | grep -q \"'model' | 'provider' | 'client' | 'tenant'\""

test_case "后端支持4个维度" \
    "grep -A5 'func isValidDimension' admin/analytics_handlers.go | grep -E -q '(model|provider|client|tenant)'"

# 3.3 字段名一致性检查
test_case "dimension字段一致" \
    "grep -q 'dimension:' web/src/api/board.ts && grep -q 'Dimension.*string.*json:\"dimension\"' admin/analytics_handlers.go"

test_case "identifier字段一致" \
    "grep -q 'identifier:' web/src/api/board.ts && grep -q 'Identifier.*string.*json:\"identifier\"' admin/analytics_handlers.go"

test_case "summary字段一致" \
    "grep -q 'summary:' web/src/api/board.ts && grep -q 'Summary.*AnalyticsSummary.*json:\"summary\"' admin/analytics_handlers.go"

test_case "trends字段一致" \
    "grep -q 'trends:' web/src/api/board.ts && grep -q 'Trends.*\[\]AnalyticsTrendPoint.*json:\"trends\"' admin/analytics_handlers.go"

test_case "cross_analysis字段一致" \
    "grep -q 'cross_analysis:' web/src/api/board.ts && grep -q 'CrossAnalysis.*AnalyticsCrossAnalysis.*json:\"cross_analysis\"' admin/analytics_handlers.go"

echo ""
echo "4. 编译验证"
echo "----------------------------------------"

# 4.1 前端编译
if cd web && npm run typecheck > /dev/null 2>&1; then
    echo -e "前端TypeScript编译 ... ${GREEN}✓ PASS${NC}"
    ((PASSED++))
    cd ..
else
    echo -e "前端TypeScript编译 ... ${RED}✗ FAIL${NC}"
    ((FAILED++))
    cd ..
fi

# 4.2 后端编译
if go build ./admin/... > /dev/null 2>&1; then
    echo -e "后端Go编译 ... ${GREEN}✓ PASS${NC}"
    ((PASSED++))
else
    echo -e "后端Go编译 ... ${RED}✗ FAIL${NC}"
    ((FAILED++))
fi

# 4.3 后端测试
if go test ./admin -run TestIsValidDimension > /dev/null 2>&1; then
    echo -e "后端单元测试 ... ${GREEN}✓ PASS${NC}"
    ((PASSED++))
else
    echo -e "后端单元测试 ... ${RED}✗ FAIL${NC}"
    ((FAILED++))
fi

echo ""
echo "5. 国际化验证"
echo "----------------------------------------"

test_case "中文文案已添加(analytics命名空间)" \
    "grep -q 'analytics:' web/src/locales/zh-CN/dashboard.ts"

test_case "中文文案已添加(board.operational)" \
    "grep -q 'board:' web/src/locales/zh-CN/index.ts"

test_case "'更多'按钮文案已定义" \
    "grep -q 'more:' web/src/locales/zh-CN/dashboard.ts"

echo ""
echo "=========================================="
echo "测试结果汇总"
echo "=========================================="
echo ""
echo -e "通过: ${GREEN}${PASSED}${NC}"
echo -e "失败: ${RED}${FAILED}${NC}"
echo -e "总计: $((PASSED + FAILED))"
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}✓ 所有测试通过!前后端契约一致,可以部署。${NC}"
    exit 0
else
    echo -e "${RED}✗ 有 ${FAILED} 个测试失败,请检查。${NC}"
    exit 1
fi
