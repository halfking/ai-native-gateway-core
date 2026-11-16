#!/bin/bash

# 测试环境验证脚本
# 用于在运行测试前检查所有前置条件

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "======================================="
echo "   测试环境验证"
echo "======================================="
echo ""

EXIT_CODE=0

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() {
    echo -e "${GREEN}✓${NC} $1"
}

fail() {
    echo -e "${RED}✗${NC} $1"
    EXIT_CODE=1
}

warn() {
    echo -e "${YELLOW}⚠${NC} $1"
}

# 1. 检查 Node.js
echo "📋 检查软件依赖..."
if command -v node &> /dev/null; then
    NODE_VERSION=$(node --version)
    pass "Node.js: $NODE_VERSION"
    
    # 检查版本是否 >= 16
    MAJOR_VERSION=$(echo $NODE_VERSION | sed 's/v//' | cut -d. -f1)
    if [ "$MAJOR_VERSION" -lt 16 ]; then
        warn "Node.js 版本较低，建议 >= 16.0.0"
    fi
else
    fail "Node.js 未安装"
    echo "   安装: https://nodejs.org/"
fi

# 2. 检查 npm
if command -v npm &> /dev/null; then
    NPM_VERSION=$(npm --version)
    pass "npm: v$NPM_VERSION"
else
    fail "npm 未安装"
fi

# 3. 检查依赖
if [ -d "node_modules" ]; then
    pass "依赖已安装"
else
    warn "依赖未安装"
    echo "   运行: npm install"
    EXIT_CODE=1
fi

# 4. 检查 Playwright
if [ -d "node_modules/@playwright" ]; then
    pass "Playwright 已安装"
else
    warn "Playwright 未安装"
    echo "   运行: npm install"
fi

echo ""

# 5. 检查环境变量
echo "🔧 检查环境变量..."

BASE_URL="${BASE_URL:-http://localhost:8090}"
if [ -z "$BASE_URL" ]; then
    warn "BASE_URL 未设置，使用默认值"
else
    pass "BASE_URL: $BASE_URL"
fi

if [ -z "$TEST_USERNAME" ]; then
    warn "TEST_USERNAME 未设置，将使用默认值 'admin'"
    export TEST_USERNAME=admin
else
    pass "TEST_USERNAME: $TEST_USERNAME"
fi

if [ -z "$TEST_PASSWORD" ]; then
    warn "TEST_PASSWORD 未设置"
    echo "   请设置: export TEST_PASSWORD=your_password"
else
    pass "TEST_PASSWORD: ******** (已设置)"
fi

echo ""

# 6. 检查服务连通性
echo "🌐 检查服务连通性..."

if command -v curl &> /dev/null; then
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL" 2>/dev/null || echo "000")
    
    if [ "$HTTP_CODE" = "200" ] || [ "$HTTP_CODE" = "302" ]; then
        pass "服务可访问: $BASE_URL (HTTP $HTTP_CODE)"
    elif [ "$HTTP_CODE" = "000" ]; then
        fail "无法连接到服务: $BASE_URL"
        echo "   请确保服务正在运行"
    else
        warn "服务返回异常状态码: HTTP $HTTP_CODE"
    fi
else
    warn "curl 未安装，跳过连通性检查"
fi

echo ""

# 7. 检查 .gitignore
echo "📝 检查项目配置..."

if [ -f ".gitignore" ]; then
    pass ".gitignore 存在"
else
    warn ".gitignore 不存在"
fi

if [ -f "playwright.config.ts" ]; then
    pass "playwright.config.ts 存在"
else
    fail "playwright.config.ts 不存在"
fi

if [ -f "package.json" ]; then
    pass "package.json 存在"
else
    fail "package.json 不存在"
fi

echo ""

# 8. 检查测试文件
echo "🧪 检查测试文件..."

TEST_COUNT=$(find . -name "*.spec.ts" -type f | wc -l | tr -d ' ')
if [ "$TEST_COUNT" -gt 0 ]; then
    pass "发现 $TEST_COUNT 个测试文件"
else
    warn "未发现测试文件"
fi

echo ""

# 9. 总结
echo "======================================="
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✅ 环境验证通过！${NC}"
    echo ""
    echo "💡 下一步:"
    echo "   1. 运行测试: ./run-test.sh"
    echo "   2. 可视化运行: ./run-test.sh headed"
    echo "   3. 查看报告: npm run test:report"
else
    echo -e "${RED}❌ 环境验证失败${NC}"
    echo ""
    echo "💡 请解决上述问题后再运行测试"
    echo ""
    echo "📚 参考文档:"
    echo "   - README.md"
    echo "   - VALIDATION-CHECKLIST.md"
fi
echo "======================================="

exit $EXIT_CODE
