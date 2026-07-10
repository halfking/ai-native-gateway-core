#!/bin/bash
# 生成本地数据库完整Schema报告
# 用于与252生产环境对比

set -e

REPORT_FILE="LOCAL_DATABASE_SCHEMA_REPORT.md"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5434}"
DB_NAME="${DB_NAME:-redclaw}"
DB_USER="${DB_USER:-redclaw}"
DB_PASSWORD="${DB_PASSWORD:-redclaw}"

echo "# 本地数据库Schema报告" > "$REPORT_FILE"
echo "" >> "$REPORT_FILE"
echo "**生成时间**: $(date '+%Y-%m-%d %H:%M:%S')" >> "$REPORT_FILE"
echo "**数据库**: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

# 1. 统计信息
echo "## 1. 统计信息" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOF' >> "$REPORT_FILE"
SELECT 
    '| 指标 | 数量 |' as header
UNION ALL
SELECT '|------|------|'
UNION ALL
SELECT '| 表总数 | ' || COUNT(*) || ' |' FROM pg_tables WHERE schemaname='public'
UNION ALL
SELECT '| 索引总数 | ' || COUNT(*) || ' |' FROM pg_indexes WHERE schemaname='public'
UNION ALL
SELECT '| 函数总数 | ' || COUNT(*) || ' |' FROM pg_proc p JOIN pg_namespace n ON p.pronamespace = n.oid WHERE n.nspname='public'
UNION ALL
SELECT '| 视图总数 | ' || COUNT(*) || ' |' FROM pg_views WHERE schemaname='public'
UNION ALL
SELECT '| 物化视图 | ' || COUNT(*) || ' |' FROM pg_matviews WHERE schemaname='public'
UNION ALL
SELECT '| 序列总数 | ' || COUNT(*) || ' |' FROM pg_sequences WHERE schemaname='public';
EOF

echo "" >> "$REPORT_FILE"

# 2. 所有表清单
echo "## 2. 表清单（按字母排序）" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"
echo '```' >> "$REPORT_FILE"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
"SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename;" >> "$REPORT_FILE"

echo '```' >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

# 3. Dashboard相关表
echo "## 3. Dashboard相关表" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"
echo '```' >> "$REPORT_FILE"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
"SELECT tablename FROM pg_tables WHERE schemaname='public' AND (tablename LIKE '%dashboard%' OR tablename LIKE '%session_module%') ORDER BY tablename;" >> "$REPORT_FILE"

echo '```' >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

# 4. 关键表的列结构
echo "## 4. 关键表结构" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

for table in auth_users users tenants applications credentials providers provider_models \
             credential_model_bindings sessions session_summaries \
             session_module_executions_hot dashboard_access_events_hot; do
    
    echo "### $table" >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    echo '```sql' >> "$REPORT_FILE"
    
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "\d $table" 2>&1 >> "$REPORT_FILE" || echo "表不存在" >> "$REPORT_FILE"
    
    echo '```' >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
done

# 5. 函数清单
echo "## 5. 函数清单" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"
echo '```' >> "$REPORT_FILE"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
"SELECT p.proname || '(' || pg_get_function_arguments(p.oid) || ')' as function_signature
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname='public'
ORDER BY p.proname;" >> "$REPORT_FILE"

echo '```' >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

# 6. 视图清单
echo "## 6. 视图清单" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"
echo '```' >> "$REPORT_FILE"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c \
"SELECT viewname FROM pg_views WHERE schemaname='public' ORDER BY viewname;" >> "$REPORT_FILE"

echo '```' >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

# 7. 扩展
echo "## 7. 已安装扩展" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"
echo '```' >> "$REPORT_FILE"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "\dx" >> "$REPORT_FILE"

echo '```' >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

echo "✓ Schema报告已生成: $REPORT_FILE"
