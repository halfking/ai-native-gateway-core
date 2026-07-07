#!/usr/bin/env bash
# ====================================================================
# 环境验证脚本 - 快速验证测试环境是否正常
# ====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

PASS=0
FAIL=0

check() {
    local name="$1"
    shift
    
    echo -n "  检查 $name ... "
    if "$@" > /dev/null 2>&1; then
        echo -e "${GREEN}✓${NC}"
        PASS=$((PASS + 1))
        return 0
    else
        echo -e "${RED}✗${NC}"
        FAIL=$((FAIL + 1))
        return 1
    fi
}

echo -e "${BLUE}=====================================${NC}"
echo -e "${BLUE}  LLM Gateway 测试环境验证${NC}"
echo -e "${BLUE}=====================================${NC}"
echo ""

echo "1. 系统依赖"
check "python3" command -v python3
check "psql" command -v psql
check "curl" command -v curl
check "jq" command -v jq
check "go" command -v go

echo ""
echo "2. Python 依赖"
check "aiohttp" python3 -c "import aiohttp"

echo ""
echo "3. 文件完整性"
check "server-v2.py" test -f "$SCRIPT_DIR/mocks/llm-mock-upstream/server-v2.py"
check "loadtest-stress.py" test -f "$SCRIPT_DIR/loadtest-stress.py"
check "mock-state-orchestrator.sh" test -f "$SCRIPT_DIR/mock-state-orchestrator.sh"
check "comprehensive-loadtest.sh" test -f "$SCRIPT_DIR/comprehensive-loadtest.sh"
check "quick-start-loadtest.sh" test -f "$SCRIPT_DIR/quick-start-loadtest.sh"

echo ""
echo "4. SQL 文件"
check "04-loadtest-mock-credentials.sql" test -f "$SCRIPT_DIR/../sql/scripts/04-loadtest-mock-credentials.sql"

echo ""
echo "5. Mock 服务 (可选 - 如果已启动)"
for port in 19080 19081 19082 19083; do
    if curl -sS --max-time 1 "http://localhost:$port/healthz" > /dev/null 2>&1; then
        echo -e "  Mock localhost:$port ... ${GREEN}✓ 运行中${NC}"
    else
        echo -e "  Mock localhost:$port ... ${YELLOW}○ 未启动${NC}"
    fi
done

echo ""
echo "6. Gateway (可选 - 如果已启动)"
if curl -sS --max-time 1 "http://localhost:8080/healthz" > /dev/null 2>&1; then
    echo -e "  Gateway localhost:8080 ... ${GREEN}✓ 运行中${NC}"
else
    echo -e "  Gateway localhost:8080 ... ${YELLOW}○ 未启动${NC}"
fi

echo ""
echo -e "${BLUE}=====================================${NC}"
echo -e "结果: ${GREEN}$PASS 通过${NC}, ${RED}$FAIL 失败${NC}"
echo -e "${BLUE}=====================================${NC}"

if [[ $FAIL -eq 0 ]]; then
    echo ""
    echo -e "${GREEN}✓ 环境验证通过！${NC}"
    echo ""
    echo "下一步:"
    echo "  1. 快速测试: ./scripts/quick-start-loadtest.sh"
    echo "  2. 完整测试: ./scripts/comprehensive-loadtest.sh all"
    echo "  3. 查看文档: cat docs/LOADTEST_GUIDE.md"
    exit 0
else
    echo ""
    echo -e "${RED}✗ 环境验证失败${NC}"
    echo ""
    echo "请检查失败的项目并安装缺失的依赖"
    echo "查看文档: docs/LOADTEST_GUIDE.md"
    exit 1
fi
