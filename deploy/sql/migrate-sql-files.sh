#!/usr/bin/env bash
# migrate-sql-files.sh
# 将项目中零散的SQL文件迁移到 deploy/sql/ 目录，按规范分类管理

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEPLOY_SQL_DIR="$SCRIPT_DIR"

echo "========================================"
echo "SQL文件迁移脚本"
echo "========================================"
echo "项目根目录: $PROJECT_ROOT"
echo "目标目录: $DEPLOY_SQL_DIR"
echo ""

# 创建目标目录结构
echo "[1/5] 创建目录结构..."
mkdir -p "$DEPLOY_SQL_DIR"/{schemas/baseline,schemas/snapshots,migrations,scripts,cron,tests,docs/features,docs/pricing,docs/experiments/archived,templates}

# 迁移 installer embeddata (baseline schema)
echo "[2/5] 迁移 installer embeddata 到 schemas/baseline/..."
if [ -d "$PROJECT_ROOT/installer/cmd/llm-gw-installer/embeddata" ]; then
    cp -v "$PROJECT_ROOT/installer/cmd/llm-gw-installer/embeddata"/*.sql "$DEPLOY_SQL_DIR/schemas/baseline/" 2>/dev/null || true
    echo "  ✓ 已复制 installer embeddata"
else
    echo "  ⚠ installer embeddata 目录不存在，跳过"
fi

# 迁移 cron jobs
echo "[3/5] 迁移 cron jobs 到 cron/..."
if [ -d "$PROJECT_ROOT/deploy/k8s/cron" ]; then
    find "$PROJECT_ROOT/deploy/k8s/cron" -name "*.sql" -exec cp -v {} "$DEPLOY_SQL_DIR/cron/" \; 2>/dev/null || true
    echo "  ✓ 已复制 cron SQL文件"
else
    echo "  ⚠ deploy/k8s/cron 目录不存在，跳过"
fi

# 迁移 tests
echo "[4/5] 迁移测试SQL到 tests/..."
if [ -d "$PROJECT_ROOT/tests" ]; then
    find "$PROJECT_ROOT/tests" -maxdepth 1 -name "*.sql" -exec cp -v {} "$DEPLOY_SQL_DIR/tests/" \; 2>/dev/null || true
    echo "  ✓ 已复制测试SQL文件"
else
    echo "  ⚠ tests 目录不存在，跳过"
fi

# 迁移 docs 中的 SQL
echo "[5/5] 迁移 docs/ 中的SQL文件..."

# 迁移 docs/*.sql 到 docs/features/
if [ -d "$PROJECT_ROOT/docs" ]; then
    find "$PROJECT_ROOT/docs" -maxdepth 1 -name "*.sql" -exec cp -v {} "$DEPLOY_SQL_DIR/docs/features/" \; 2>/dev/null || true
    echo "  ✓ 已复制 docs/*.sql 到 features/"
fi

# 迁移 docs/pricing/*.sql
if [ -d "$PROJECT_ROOT/docs/pricing" ]; then
    find "$PROJECT_ROOT/docs/pricing" -name "*.sql" -exec cp -v {} "$DEPLOY_SQL_DIR/docs/pricing/" \; 2>/dev/null || true
    echo "  ✓ 已复制 docs/pricing/*.sql"
fi

echo ""
echo "========================================"
echo "迁移完成统计"
echo "========================================"

echo ""
echo "schemas/baseline/: $(find "$DEPLOY_SQL_DIR/schemas/baseline" -name "*.sql" 2>/dev/null | wc -l | xargs) 个文件"
echo "cron/: $(find "$DEPLOY_SQL_DIR/cron" -name "*.sql" 2>/dev/null | wc -l | xargs) 个文件"
echo "tests/: $(find "$DEPLOY_SQL_DIR/tests" -name "*.sql" 2>/dev/null | wc -l | xargs) 个文件"
echo "docs/features/: $(find "$DEPLOY_SQL_DIR/docs/features" -name "*.sql" 2>/dev/null | wc -l | xargs) 个文件"
echo "docs/pricing/: $(find "$DEPLOY_SQL_DIR/docs/pricing" -name "*.sql" 2>/dev/null | wc -l | xargs) 个文件"

echo ""
echo "总计: $(find "$DEPLOY_SQL_DIR" -name "*.sql" 2>/dev/null | wc -l | xargs) 个SQL文件"
echo ""
echo "✓ 迁移完成！"
echo ""
echo "下一步："
echo "1. 检查 deploy/sql/ 目录结构"
echo "2. 识别重复文件并合并"
echo "3. 生成 README.md 和部署方案文档"
