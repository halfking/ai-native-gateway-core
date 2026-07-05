#!/usr/bin/env bash
# verify-migration.sh
# 验证 deploy/sql/ 迁移结果

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "========================================"
echo "deploy/sql/ 迁移验证"
echo "========================================"
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试计数
TESTS_TOTAL=0
TESTS_PASSED=0
TESTS_FAILED=0

# 测试函数
test_case() {
    local name="$1"
    local condition="$2"
    
    TESTS_TOTAL=$((TESTS_TOTAL + 1))
    
    if eval "$condition"; then
        echo -e "${GREEN}✓${NC} $name"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} $name"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

echo "[1/6] 验证目录结构..."
echo ""

test_case "存在 schemas/baseline/ 目录" "[ -d '$SCRIPT_DIR/schemas/baseline' ]"
test_case "存在 schemas/snapshots/ 目录" "[ -d '$SCRIPT_DIR/schemas/snapshots' ]"
test_case "存在 migrations/ 目录" "[ -d '$SCRIPT_DIR/migrations' ]"
test_case "存在 scripts/ 目录" "[ -d '$SCRIPT_DIR/scripts' ]"
test_case "存在 cron/ 目录" "[ -d '$SCRIPT_DIR/cron' ]"
test_case "存在 tests/ 目录" "[ -d '$SCRIPT_DIR/tests' ]"
test_case "存在 docs/features/ 目录" "[ -d '$SCRIPT_DIR/docs/features' ]"
test_case "存在 docs/pricing/ 目录" "[ -d '$SCRIPT_DIR/docs/pricing' ]"
test_case "存在 templates/ 目录" "[ -d '$SCRIPT_DIR/templates' ]"

echo ""
echo "[2/6] 验证关键文件..."
echo ""

test_case "存在 README.md" "[ -f '$SCRIPT_DIR/README.md' ]"
test_case "存在 DEPLOYMENT_PLAN.md" "[ -f '$SCRIPT_DIR/DEPLOYMENT_PLAN.md' ]"
test_case "存在 STRUCTURE_PLAN.md" "[ -f '$SCRIPT_DIR/STRUCTURE_PLAN.md' ]"
test_case "存在 migrate-sql-files.sh" "[ -f '$SCRIPT_DIR/migrate-sql-files.sh' ]"

echo ""
echo "[3/6] 验证 baseline schema..."
echo ""

test_case "存在 00-prereqs.sql" "[ -f '$SCRIPT_DIR/schemas/baseline/00-prereqs.sql' ]"
test_case "存在 01-schema.sql" "[ -f '$SCRIPT_DIR/schemas/baseline/01-schema.sql' ]"
test_case "存在 02-seed.sql" "[ -f '$SCRIPT_DIR/schemas/baseline/02-seed.sql' ]"

# 验证与 sql/schema/ 的一致性
if [ -f "$PROJECT_ROOT/sql/schema/00-prereqs.sql" ]; then
    test_case "00-prereqs.sql 与 sql/schema/ 一致" \
        "diff -q '$SCRIPT_DIR/schemas/baseline/00-prereqs.sql' '$PROJECT_ROOT/sql/schema/00-prereqs.sql' >/dev/null 2>&1"
fi

if [ -f "$PROJECT_ROOT/sql/schema/01-schema.sql" ]; then
    test_case "01-schema.sql 与 sql/schema/ 一致" \
        "diff -q '$SCRIPT_DIR/schemas/baseline/01-schema.sql' '$PROJECT_ROOT/sql/schema/01-schema.sql' >/dev/null 2>&1"
fi

if [ -f "$PROJECT_ROOT/sql/schema/02-seed.sql" ]; then
    test_case "02-seed.sql 与 sql/schema/ 一致" \
        "diff -q '$SCRIPT_DIR/schemas/baseline/02-seed.sql' '$PROJECT_ROOT/sql/schema/02-seed.sql' >/dev/null 2>&1"
fi

echo ""
echo "[4/6] 验证 SQL 文件迁移..."
echo ""

# 统计文件数量
BASELINE_COUNT=$(find "$SCRIPT_DIR/schemas/baseline" -name "*.sql" 2>/dev/null | wc -l | xargs)
CRON_COUNT=$(find "$SCRIPT_DIR/cron" -name "*.sql" 2>/dev/null | wc -l | xargs)
TESTS_COUNT=$(find "$SCRIPT_DIR/tests" -name "*.sql" 2>/dev/null | wc -l | xargs)
FEATURES_COUNT=$(find "$SCRIPT_DIR/docs/features" -name "*.sql" 2>/dev/null | wc -l | xargs)
PRICING_COUNT=$(find "$SCRIPT_DIR/docs/pricing" -name "*.sql" 2>/dev/null | wc -l | xargs)

test_case "schemas/baseline/ 有 3 个文件" "[ '$BASELINE_COUNT' -eq 3 ]"
test_case "cron/ 至少有 1 个文件" "[ '$CRON_COUNT' -ge 1 ]"
test_case "tests/ 至少有 1 个文件" "[ '$TESTS_COUNT' -ge 1 ]"
test_case "docs/features/ 至少有 10 个文件" "[ '$FEATURES_COUNT' -ge 10 ]"
test_case "docs/pricing/ 至少有 1 个文件" "[ '$PRICING_COUNT' -ge 1 ]"

echo ""
echo "[5/6] 验证 SQL 文件语法..."
echo ""

# 检查 SQL 文件是否有基本的语法结构
if command -v grep >/dev/null 2>&1; then
    # 检查 baseline schema 文件
    if grep -q "CREATE" "$SCRIPT_DIR/schemas/baseline/01-schema.sql" 2>/dev/null; then
        test_case "01-schema.sql 包含 CREATE 语句" "true"
    else
        test_case "01-schema.sql 包含 CREATE 语句" "false"
    fi
    
    if grep -q "INSERT" "$SCRIPT_DIR/schemas/baseline/02-seed.sql" 2>/dev/null; then
        test_case "02-seed.sql 包含 INSERT 语句" "true"
    else
        test_case "02-seed.sql 包含 INSERT 语句" "false"
    fi
else
    echo -e "${YELLOW}⚠${NC} 跳过 SQL 语法验证（grep 命令不可用）"
fi

echo ""
echo "[6/7] 验证 objects/ 对象文件..."
echo ""

# 检查 objects/ 目录
test_case "存在 objects/ 目录" "[ -d '$SCRIPT_DIR/objects' ]"

# 检查各类对象目录
for obj_type in tables views functions sequences triggers indexes constraints policies; do
    test_case "存在 objects/$obj_type/ 目录" "[ -d '$SCRIPT_DIR/objects/$obj_type' ]"
done

# 统计对象文件数量
if [ -d "$SCRIPT_DIR/objects" ]; then
    TABLES_COUNT=$(find "$SCRIPT_DIR/objects/tables" -name "*.sql" 2>/dev/null | wc -l | xargs)
    VIEWS_COUNT=$(find "$SCRIPT_DIR/objects/views" -name "*.sql" 2>/dev/null | wc -l | xargs)
    FUNCTIONS_COUNT=$(find "$SCRIPT_DIR/objects/functions" -name "*.sql" 2>/dev/null | wc -l | xargs)
    SEQUENCES_COUNT=$(find "$SCRIPT_DIR/objects/sequences" -name "*.sql" 2>/dev/null | wc -l | xargs)
    TRIGGERS_COUNT=$(find "$SCRIPT_DIR/objects/triggers" -name "*.sql" 2>/dev/null | wc -l | xargs)
    INDEXES_COUNT=$(find "$SCRIPT_DIR/objects/indexes" -name "*.sql" 2>/dev/null | wc -l | xargs)
    CONSTRAINTS_COUNT=$(find "$SCRIPT_DIR/objects/constraints" -name "*.sql" 2>/dev/null | wc -l | xargs)
    POLICIES_COUNT=$(find "$SCRIPT_DIR/objects/policies" -name "*.sql" 2>/dev/null | wc -l | xargs)
    
    test_case "objects/tables/ 有 103 个文件" "[ '$TABLES_COUNT' -eq 103 ]"
    test_case "objects/views/ 有 9 个文件" "[ '$VIEWS_COUNT' -eq 9 ]"
    test_case "objects/functions/ 有 18 个文件" "[ '$FUNCTIONS_COUNT' -eq 18 ]"
    test_case "objects/sequences/ 有 113 个文件" "[ '$SEQUENCES_COUNT' -eq 113 ]"
    test_case "objects/triggers/ 有 14 个文件" "[ '$TRIGGERS_COUNT' -eq 14 ]"
    test_case "objects/indexes/ 有 425 个文件" "[ '$INDEXES_COUNT' -eq 425 ]"
    test_case "objects/constraints/ 有 127 个文件" "[ '$CONSTRAINTS_COUNT' -eq 127 ]"
    test_case "objects/policies/ 有 30 个文件" "[ '$POLICIES_COUNT' -eq 30 ]"
fi

echo ""
echo "[7/7] 验证文档完整性..."
echo ""

# 检查 README.md 是否包含关键章节
if grep -q "目录结构" "$SCRIPT_DIR/README.md" 2>/dev/null; then
    test_case "README.md 包含'目录结构'章节" "true"
else
    test_case "README.md 包含'目录结构'章节" "false"
fi

if grep -q "部署流程" "$SCRIPT_DIR/README.md" 2>/dev/null; then
    test_case "README.md 包含'部署流程'章节" "true"
else
    test_case "README.md 包含'部署流程'章节" "false"
fi

# 检查 DEPLOYMENT_PLAN.md 是否包含关键章节
if grep -q "部署前置条件" "$SCRIPT_DIR/DEPLOYMENT_PLAN.md" 2>/dev/null; then
    test_case "DEPLOYMENT_PLAN.md 包含'部署前置条件'章节" "true"
else
    test_case "DEPLOYMENT_PLAN.md 包含'部署前置条件'章节" "false"
fi

if grep -q "回滚方案" "$SCRIPT_DIR/DEPLOYMENT_PLAN.md" 2>/dev/null; then
    test_case "DEPLOYMENT_PLAN.md 包含'回滚方案'章节" "true"
else
    test_case "DEPLOYMENT_PLAN.md 包含'回滚方案'章节" "false"
fi

echo ""
echo "========================================"
echo "验证总结"
echo "========================================"
echo ""
echo "总测试数: $TESTS_TOTAL"
echo -e "${GREEN}通过: $TESTS_PASSED${NC}"
if [ $TESTS_FAILED -gt 0 ]; then
    echo -e "${RED}失败: $TESTS_FAILED${NC}"
else
    echo "失败: $TESTS_FAILED"
fi
echo ""

# 文件统计
echo "文件统计:"
echo "  schemas/baseline/: $BASELINE_COUNT 个文件"
echo "  cron/: $CRON_COUNT 个文件"
echo "  tests/: $TESTS_COUNT 个文件"
echo "  docs/features/: $FEATURES_COUNT 个文件"
echo "  docs/pricing/: $PRICING_COUNT 个文件"

if [ -d "$SCRIPT_DIR/objects" ]; then
    echo ""
    echo "  objects/ 对象统计:"
    for obj_type in tables views functions sequences triggers indexes constraints policies; do
        if [ -d "$SCRIPT_DIR/objects/$obj_type" ]; then
            count=$(find "$SCRIPT_DIR/objects/$obj_type" -name "*.sql" 2>/dev/null | wc -l | xargs)
            printf "    %-15s: %4d 个文件\n" "$obj_type" "$count"
        fi
    done
fi

echo ""
echo "  总计: $(find "$SCRIPT_DIR" -name "*.sql" 2>/dev/null | wc -l | xargs) 个 SQL 文件"
echo ""

# 目录大小
if command -v du >/dev/null 2>&1; then
    echo "目录大小:"
    du -sh "$SCRIPT_DIR" 2>/dev/null || echo "  无法计算"
    echo ""
fi

if [ $TESTS_FAILED -eq 0 ]; then
    echo -e "${GREEN}✓ 所有验证通过！${NC}"
    echo ""
    echo "下一步："
    echo "1. 查看 deploy/sql/README.md 了解使用方法"
    echo "2. 查看 deploy/sql/DEPLOYMENT_PLAN.md 了解部署流程"
    echo "3. 执行 'git status' 查看变更"
    echo "4. 提交变更到 Git 仓库"
    exit 0
else
    echo -e "${RED}✗ 验证失败，请检查上述错误${NC}"
    exit 1
fi
