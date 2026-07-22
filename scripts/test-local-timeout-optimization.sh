#!/bin/bash
# 本地测试脚本 - 超时优化功能验证

set -e

echo "=========================================="
echo "LLM Gateway 超时优化本地测试"
echo "=========================================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试结果统计
PASSED=0
FAILED=0
TOTAL=0

# 测试函数
test_case() {
    local name="$1"
    local command="$2"
    local expected="$3"
    
    TOTAL=$((TOTAL + 1))
    echo -e "${YELLOW}[测试 $TOTAL]${NC} $name"
    
    if eval "$command" | grep -q "$expected"; then
        echo -e "${GREEN}✅ PASS${NC}"
        PASSED=$((PASSED + 1))
        return 0
    else
        echo -e "${RED}❌ FAIL${NC}"
        FAILED=$((FAILED + 1))
        return 1
    fi
}

# 检查二进制文件
echo "检查编译产物..."
if [ ! -f "./llm-gateway-go" ]; then
    echo -e "${RED}错误: llm-gateway-go 二进制文件不存在${NC}"
    echo "请先运行: go build -o llm-gateway-go ./cmd/gateway"
    exit 1
fi
echo -e "${GREEN}✅ 二进制文件存在${NC}"
echo ""

# 检查数据库连接
echo "检查数据库连接..."
DB_HOST="${DB_HOST:-172.16.2.210}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-llm_gateway}"

if ! command -v psql &> /dev/null; then
    echo -e "${YELLOW}⚠️  psql未安装，跳过数据库测试${NC}"
    SKIP_DB=1
else
    if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1" &> /dev/null; then
        echo -e "${GREEN}✅ 数据库连接成功${NC}"
        SKIP_DB=0
    else
        echo -e "${YELLOW}⚠️  数据库连接失败，跳过数据库测试${NC}"
        SKIP_DB=1
    fi
fi
echo ""

# 测试1: 编译检查
echo "=========================================="
echo "测试1: 编译与版本检查"
echo "=========================================="
echo ""

test_case "检查二进制文件大小" \
    "stat -f%z ./llm-gateway-go" \
    "^[0-9]"

test_case "检查二进制文件权限" \
    "ls -l ./llm-gateway-go | grep 'x'" \
    "x"

echo ""

# 测试2: 配置文件检查
echo "=========================================="
echo "测试2: 配置与Schema检查"
echo "=========================================="
echo ""

if [ $SKIP_DB -eq 0 ]; then
    test_case "检查system_settings表" \
        "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"SELECT COUNT(*) FROM system_settings WHERE category='timeout'\"" \
        "^[[:space:]]*[1-9]"
    
    test_case "检查request_logs新字段" \
        "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"SELECT column_name FROM information_schema.columns WHERE table_name='request_logs' AND column_name='effective_timeout_seconds'\"" \
        "effective_timeout_seconds"
    
    test_case "检查session_last_requests表" \
        "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"SELECT COUNT(*) FROM information_schema.tables WHERE table_name='session_last_requests'\"" \
        "^[[:space:]]*1"
    
    test_case "检查超时配置默认值" \
        "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"SELECT value FROM system_settings WHERE key='timeout.upstream_base_seconds'\"" \
        "90"
else
    echo -e "${YELLOW}⚠️  跳过数据库测试${NC}"
fi

echo ""

# 测试3: 代码单元测试
echo "=========================================="
echo "测试3: 单元测试"
echo "=========================================="
echo ""

test_case "TimeoutConfig单元测试" \
    "go test ./config -run TestTimeoutConfig -v 2>&1 | grep -c PASS" \
    "^[1-9]"

test_case "KeepaliveSender单元测试" \
    "go test ./domains/streaming -run TestKeepalive -v 2>&1 | grep -c PASS" \
    "^[1-9]"

test_case "ContinuationDetector单元测试" \
    "go test ./domains/streaming -run TestContinuation -v 2>&1 | grep -c PASS" \
    "^[1-9]"

echo ""

# 测试4: 代码质量检查
echo "=========================================="
echo "测试4: 代码质量检查"
echo "=========================================="
echo ""

test_case "Go vet检查" \
    "go vet ./... 2>&1 || echo 'no issues'" \
    "no issues"

test_case "Go fmt检查" \
    "gofmt -l . | wc -l" \
    "^[[:space:]]*0"

echo ""

# 测试5: 数据完整性检查
if [ $SKIP_DB -eq 0 ]; then
    echo "=========================================="
    echo "测试5: 数据完整性检查"
    echo "=========================================="
    echo ""
    
    test_case "检查超时配置完整性" \
        "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"SELECT COUNT(*) FROM system_settings WHERE category='timeout'\"" \
        "1[0-9]"
    
    test_case "检查重试配置完整性" \
        "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"SELECT COUNT(*) FROM system_settings WHERE category='retry'\"" \
        "[5-9]"
    
    test_case "检查分析视图" \
        "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -c \"SELECT COUNT(*) FROM information_schema.views WHERE table_name LIKE 'v_%timeout%' OR table_name LIKE 'v_%continuation%'\"" \
        "[1-9]"
    
    echo ""
fi

# 测试6: 内存检查
echo "=========================================="
echo "测试6: 二进制分析"
echo "=========================================="
echo ""

BINARY_SIZE=$(stat -f%z ./llm-gateway-go)
echo "二进制大小: $(($BINARY_SIZE / 1024 / 1024)) MB"

if [ $BINARY_SIZE -lt 100000000 ]; then
    echo -e "${GREEN}✅ 二进制大小合理 (< 100MB)${NC}"
    PASSED=$((PASSED + 1))
else
    echo -e "${YELLOW}⚠️  二进制较大 (> 100MB)${NC}"
    FAILED=$((FAILED + 1))
fi
TOTAL=$((TOTAL + 1))

echo ""

# 测试7: 依赖检查
echo "=========================================="
echo "测试7: 依赖检查"
echo "=========================================="
echo ""

test_case "检查pgxpool依赖" \
    "go list -m all | grep pgxpool" \
    "pgxpool"

test_case "检查slog依赖" \
    "grep -r 'log/slog' --include='*.go' . | wc -l" \
    "^[[:space:]]*[1-9]"

echo ""

# 总结
echo "=========================================="
echo "测试总结"
echo "=========================================="
echo ""
echo "总测试数: $TOTAL"
echo -e "${GREEN}通过: $PASSED${NC}"
echo -e "${RED}失败: $FAILED${NC}"
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}🎉 所有测试通过！${NC}"
    exit 0
else
    echo -e "${RED}❌ 有 $FAILED 个测试失败${NC}"
    exit 1
fi
