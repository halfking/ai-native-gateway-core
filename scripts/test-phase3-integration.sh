#!/usr/bin/env bash
# Phase 3 集成测试脚本

set -euo pipefail

echo "========================================="
echo "Phase 3 集成测试"
echo "========================================="
echo ""

# ============================================================================
# 步骤 1: 检查数据库连接
# ============================================================================

echo "📋 步骤 1: 检查数据库连接（252 Docker PG17）"
echo ""

# 数据库连接信息
DB_HOST="172.16.2.210"
DB_PORT="4100"
DB_NAME="maintain"
DB_USER="postgres"
DB_PASSWORD="${POSTGRES_PASSWORD:-your_password}"

echo "数据库信息:"
echo "  主机: $DB_HOST"
echo "  端口: $DB_PORT"
echo "  数据库: $DB_NAME"
echo "  用户: $DB_USER"
echo ""

# 测试连接
echo "测试连接..."
if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT version();" > /dev/null 2>&1; then
    echo "   ✅ 数据库连接成功"
    
    # 显示PostgreSQL版本
    VERSION=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT version();")
    echo "   版本: $(echo $VERSION | grep -oP 'PostgreSQL \d+\.\d+')"
else
    echo "   ❌ 数据库连接失败"
    echo ""
    echo "请设置正确的数据库密码:"
    echo "  export POSTGRES_PASSWORD=your_password"
    exit 1
fi

# ============================================================================
# 步骤 2: 执行数据库迁移
# ============================================================================

echo ""
echo "📦 步骤 2: 执行数据库迁移"
echo ""

MIGRATION_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/db/migrations/014_create_releases_tables.sql"

if [[ ! -f "$MIGRATION_FILE" ]]; then
    echo "   ❌ 迁移文件不存在: $MIGRATION_FILE"
    exit 1
fi

echo "迁移文件: $MIGRATION_FILE"
echo ""

# 检查表是否已存在
TABLES_EXIST=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "SELECT COUNT(*) FROM information_schema.tables WHERE table_name IN ('releases', 'release_files', 'release_tests');")

if [[ "$TABLES_EXIST" -eq 3 ]]; then
    echo "   ℹ️  表已存在，跳过迁移"
    echo ""
    read -p "是否重新创建表（将删除现有数据）？(yes/no) " -r
    if [[ $REPLY =~ ^[Yy]es$ ]]; then
        echo "   删除现有表..."
        PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << SQL
DROP TABLE IF EXISTS release_tests CASCADE;
DROP TABLE IF EXISTS release_files CASCADE;
DROP TABLE IF EXISTS releases CASCADE;
DROP VIEW IF EXISTS v_releases_overview CASCADE;
SQL
        echo "   ✅ 现有表已删除"
    else
        echo "   跳过迁移，继续测试"
        SKIP_MIGRATION=true
    fi
fi

if [[ "${SKIP_MIGRATION:-false}" != "true" ]]; then
    echo "执行迁移..."
    if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$MIGRATION_FILE"; then
        echo "   ✅ 数据库迁移成功"
    else
        echo "   ❌ 数据库迁移失败"
        exit 1
    fi
fi

# ============================================================================
# 步骤 3: 验证表结构
# ============================================================================

echo ""
echo "🔍 步骤 3: 验证表结构"
echo ""

# 检查表
TABLES=(releases release_files release_tests)

for table in "${TABLES[@]}"; do
    COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT COUNT(*) FROM information_schema.tables WHERE table_name='$table';")
    
    if [[ "$COUNT" -eq 1 ]]; then
        echo "   ✅ 表存在: $table"
        
        # 显示列数
        COL_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='$table';")
        echo "      列数: $COL_COUNT"
    else
        echo "   ❌ 表不存在: $table"
        exit 1
    fi
done

# 检查视图
VIEW_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "SELECT COUNT(*) FROM information_schema.views WHERE table_name='v_releases_overview';")

if [[ "$VIEW_COUNT" -eq 1 ]]; then
    echo "   ✅ 视图存在: v_releases_overview"
else
    echo "   ❌ 视图不存在: v_releases_overview"
    exit 1
fi

# ============================================================================
# 步骤 4: 测试数据操作
# ============================================================================

echo ""
echo "🧪 步骤 4: 测试数据操作"
echo ""

# 4.1 插入测试版本
echo "4.1 插入测试版本..."
RELEASE_ID=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "INSERT INTO releases (
        version, full_version, build_seq, git_sha, build_date,
        release_type, release_notes, status, is_public,
        min_postgres_version, min_redis_version
    ) VALUES (
        '2.4.7', '2.4.7-1347-test-$(date +%Y%m%d%H%M%S)', 1347, 'test123', '$(date +%Y%m%d)',
        'stable', '测试版本', 'draft', false,
        '14', '6'
    ) RETURNING id;" | tr -d ' ')

if [[ -n "$RELEASE_ID" ]]; then
    echo "   ✅ 版本创建成功，ID: $RELEASE_ID"
else
    echo "   ❌ 版本创建失败"
    exit 1
fi

# 4.2 添加文件
echo ""
echo "4.2 添加文件记录..."
FILE_ID=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "INSERT INTO release_files (
        release_id, filename, file_path, file_size, file_hash,
        platform, os, arch
    ) VALUES (
        $RELEASE_ID, 'test-linux-amd64.tar.gz', '/test/path', 52428800, 'abc123',
        'host', 'linux', 'amd64'
    ) RETURNING id;" | tr -d ' ')

if [[ -n "$FILE_ID" ]]; then
    echo "   ✅ 文件添加成功，ID: $FILE_ID"
else
    echo "   ❌ 文件添加失败"
    exit 1
fi

# 4.3 添加测试记录
echo ""
echo "4.3 添加测试记录..."
TEST_ID=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "INSERT INTO release_tests (
        release_id, test_type, test_name, test_status,
        test_duration, test_environment
    ) VALUES (
        $RELEASE_ID, 'unit', '单元测试', 'passed',
        120, 'local'
    ) RETURNING id;" | tr -d ' ')

if [[ -n "$TEST_ID" ]]; then
    echo "   ✅ 测试记录添加成功，ID: $TEST_ID"
else
    echo "   ❌ 测试记录添加失败"
    exit 1
fi

# 4.4 查询概览视图
echo ""
echo "4.4 查询概览视图..."
OVERVIEW=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "SELECT version, file_count, passed_tests, total_tests FROM v_releases_overview WHERE id=$RELEASE_ID;")

if [[ -n "$OVERVIEW" ]]; then
    echo "   ✅ 概览查询成功"
    echo "      $OVERVIEW"
else
    echo "   ❌ 概览查询失败"
    exit 1
fi

# 4.5 清理测试数据
echo ""
echo "4.5 清理测试数据..."
read -p "是否删除测试数据？(yes/no) " -r
if [[ $REPLY =~ ^[Yy]es$ ]]; then
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
        "DELETE FROM releases WHERE id=$RELEASE_ID;" > /dev/null
    echo "   ✅ 测试数据已清理"
else
    echo "   ℹ️  保留测试数据（ID: $RELEASE_ID）"
fi

# ============================================================================
# 步骤 5: 生成测试报告
# ============================================================================

echo ""
echo "📊 步骤 5: 生成测试报告"
echo ""

# 统计信息
TOTAL_RELEASES=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "SELECT COUNT(*) FROM releases;" | tr -d ' ')

TOTAL_FILES=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "SELECT COUNT(*) FROM release_files;" | tr -d ' ')

TOTAL_TESTS=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
    "SELECT COUNT(*) FROM release_tests;" | tr -d ' ')

echo "数据库统计:"
echo "  版本数: $TOTAL_RELEASES"
echo "  文件数: $TOTAL_FILES"
echo "  测试记录数: $TOTAL_TESTS"

# ============================================================================
# 汇总
# ============================================================================

echo ""
echo "========================================="
echo "✅ Phase 3 集成测试完成"
echo "========================================="
echo ""
echo "测试结果:"
echo "  ✅ 数据库连接"
echo "  ✅ 表结构验证（3表+1视图）"
echo "  ✅ 数据插入操作"
echo "  ✅ 数据查询操作"
echo "  ✅ 视图查询"
echo ""
echo "数据库信息:"
echo "  主机: $DB_HOST:$DB_PORT"
echo "  数据库: $DB_NAME"
echo "  版本: PostgreSQL 17"
echo ""

