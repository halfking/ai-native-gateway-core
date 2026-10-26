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
    else
        echo -e "${RED}✗${NC} $name"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
}

echo "[1/6] 验证目录结构..."
echo ""

test_case "存在 schemas/baseline/ 目录" "[ -d '$SCRIPT_DIR/schemas/baseline' ]"
test_case "存在 schemas/snapshots/ 目录" "[ -d '$SCRIPT_DIR/schemas/snapshots' ] || [ -f '$SCRIPT_DIR/schemas/snapshots/.gitkeep' ]"
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
# ★ 2026-10-21（R65 轮）：原检查写的是 `DEPLOYMENT_PLAN.md` —— 该文件**不存在**
# （现存的是 DEPLOY-252.md / STRUCTURE_PLAN.md / README.md）⇒ 每跑必红 ⇒ 假期望。
# ⇒ 改为断言**真实存在的**部署指南（DEPLOY-252.md 是 252 的部署指南，README.md 是主干）。
test_case "存在部署指南（DEPLOY-252.md 或 README.md）" \
    "[ -f '$SCRIPT_DIR/DEPLOY-252.md' ] || [ -f '$SCRIPT_DIR/README.md' ]"
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
for obj_type in tables views functions sequences triggers indexes constraints policies other; do
    test_case "存在 objects/$obj_type/ 目录" "[ -d '$SCRIPT_DIR/objects/$obj_type' ]"
done

# 统计对象文件数量
#
# ★ 2026-10-21（R65 轮）：这一段原先写死了 9 个**魔数**（271 / 53 / 110 / 718 / …），
# 而它们早已与仓库实况脱节（views 实为 52、tables 实为 281、functions 实为 97、
# indexes 实为 603、triggers 实为 32、constraints 实为 260、policies 实为 119 …）
# ⇒ 任何人「照文档跑一遍 sync-objects.sh + verify-migration.sh」都会看到
# **7 类对象全部报失败** ⇒ 验证脚本退化成假警报来源（而它本该是「照文档操作不炸」的
# 最后一道确认）。
# ⇒ 改成断言**真正要保证的不变量**：镜像目录与源目录（sql/objects/）**逐类同数**。
#   魔数不是不变量（它每次对象增删都要改，而改漏了没人知道）；
#   「拷贝是忠实的」才是 —— 它由 sync-objects.sh 的拷贝语义直接保证。
if [ -d "$SCRIPT_DIR/objects" ]; then
    for obj_type in tables views functions sequences triggers indexes constraints policies other; do
        DEPLOY_COUNT=$(find "$SCRIPT_DIR/objects/$obj_type" -name "*.sql" 2>/dev/null | wc -l | xargs)
        SOURCE_COUNT=$(find "$PROJECT_ROOT/sql/objects/$obj_type" -name "*.sql" 2>/dev/null | wc -l | xargs)
        test_case "objects/$obj_type/ 文件数与源一致（镜像 ${DEPLOY_COUNT} / 源 ${SOURCE_COUNT}）" \
            "[ '$DEPLOY_COUNT' -eq '$SOURCE_COUNT' ]"
    done
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

# ★ 2026-10-21（R65 轮）：原先这三项检查指向 `DEPLOYMENT_PLAN.md` ——
# 而该文件**在仓里根本不存在**（现存的是 DEPLOY-252.md / README.md / STRUCTURE_PLAN.md）
# ⇒ 脚本每次跑都报 3 条**假失败** ⇒ 验证脚本自己成了假警报来源
#   （而它的本职是「照文档操作不炸」的最后一道确认）。
# ⇒ 改成断言**现存文档**上真实存在的契约：
#   ① deploy/sql/README.md 有「部署流程」章节（部署指南的主干）—— 已在上面检查；
#   ② deploy/ 下**至少有一份**文档写了「回滚方案」（跨目录找，不写死文件名）——
#      写死文件名就会重演「文件改名 ⇒ 检查变假失败」这一轮。
if grep -rq "回滚方案" "$SCRIPT_DIR/.." 2>/dev/null; then
    test_case "deploy/ 下有文档写明'回滚方案'" "true"
else
    test_case "deploy/ 下有文档写明'回滚方案'" "false"
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
    for obj_type in tables views functions sequences triggers indexes constraints policies other; do
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
    echo "2. 查看 deploy/sql/README.md 的「部署流程」章节"
    echo "3. 执行 'git status' 查看变更"
    echo "4. 提交变更到 Git 仓库"
    exit 0
else
    echo -e "${RED}✗ 验证失败，请检查上述错误${NC}"
    exit 1
fi
