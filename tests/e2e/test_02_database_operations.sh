#!/usr/bin/env bash
# E2E Test 02: 数据库操作测试

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_LIB="$PROJECT_ROOT/tests/lib/common.sh"

source "$TEST_LIB"

print_test_header "E2E Test 02: 数据库操作验证"

DB_PASS=$(bash ~/workspace/ai-native-tools/envs/loader.sh query COMMON_PG_SUPERUSER_PASS 2>/dev/null || echo "")

if [ -z "$DB_PASS" ]; then
    skip_test_case "数据库连接" "无法获取数据库密码"
    skip_test_case "插入版本记录" "依赖数据库连接"
    skip_test_case "插入关联数据" "依赖数据库连接"
    skip_test_case "视图查询" "依赖数据库连接"
    skip_test_case "更新版本状态" "依赖数据库连接"
    skip_test_case "级联删除" "依赖数据库连接"
    print_test_summary
    exit 0
fi

# 测试 2.1: 数据库连接
print_step "测试数据库连接"
if ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -c 'SELECT 1;'" > /tmp/db_test.log 2>&1; then
    record_test_result "数据库连接" "PASS" "0"
else
    record_test_result "数据库连接" "FAIL" "0"
fi

# 测试 2.2: 插入版本记录
print_step "测试插入版本"
TEST_VERSION="2.4.8-e2e-$(date +%s)"
SQL_INSERT="INSERT INTO releases (version, full_version, build_seq, git_sha, build_date, release_type, release_notes, status, is_public, min_postgres_version, min_redis_version, created_by) VALUES ('2.4.8', '$TEST_VERSION', 9999, 'e2e123', '$(date +%Y%m%d)', 'beta', 'E2E测试版本', 'draft', true, '14', '6', 'e2e-test') RETURNING id;"

INSERT_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -t -c \"$SQL_INSERT\"" 2>&1)

RELEASE_ID=$(echo "$INSERT_RESULT" | grep -oE "^[ ]*[0-9]+" | head -1 | tr -d ' ')

if [ -n "$RELEASE_ID" ] && [ "$RELEASE_ID" -gt 0 ]; then
    record_test_result "插入版本记录" "PASS" "0"
    print_info "版本ID: $RELEASE_ID"
else
    record_test_result "插入版本记录" "FAIL" "0"
    print_error "插入失败: $INSERT_RESULT"
    print_test_summary
    exit 1
fi

# 测试 2.3: 插入关联文件
print_step "测试插入文件"
SQL_FILE="INSERT INTO release_files (release_id, filename, file_path, file_size, file_hash, platform, os, arch, download_url) VALUES ($RELEASE_ID, 'e2e-test-linux-amd64.tar.gz', '/e2e/', 102400, 'e2e123', 'host', 'linux', 'amd64', 'https://test.com/file') RETURNING id;"

FILE_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -t -c \"$SQL_FILE\"" 2>&1)

if echo "$FILE_RESULT" | grep -q "[0-9]"; then
    record_test_result "插入文件" "PASS" "0"
else
    record_test_result "插入文件" "FAIL" "0"
fi

# 测试 2.4: 插入测试记录
print_step "测试插入测试记录"
SQL_TEST="INSERT INTO release_tests (release_id, test_type, test_name, test_status, test_duration, test_environment, tester) VALUES ($RELEASE_ID, 'e2e', 'E2E测试', 'passed', 60, 'test', 'e2e-runner') RETURNING id;"

TEST_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -t -c \"$SQL_TEST\"" 2>&1)

if echo "$TEST_RESULT" | grep -q "[0-9]"; then
    record_test_result "插入测试记录" "PASS" "0"
else
    record_test_result "插入测试记录" "FAIL" "0"
fi

# 测试 2.5: 视图查询
print_step "测试视图查询"
SQL_VIEW="SELECT version, file_count, passed_tests FROM v_releases_overview WHERE id = $RELEASE_ID;"

VIEW_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -c \"$SQL_VIEW\"" 2>&1)

if echo "$VIEW_RESULT" | grep -q "2.4.8"; then
    record_test_result "视图查询" "PASS" "0"
else
    record_test_result "视图查询" "FAIL" "0"
fi

# 测试 2.6: 更新状态
print_step "测试更新操作"
SQL_UPDATE="UPDATE releases SET status = 'published', updated_by = 'e2e-test' WHERE id = $RELEASE_ID RETURNING id, status;"

UPDATE_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -t -c \"$SQL_UPDATE\"" 2>&1)

if echo "$UPDATE_RESULT" | grep -q "published"; then
    record_test_result "更新版本状态" "PASS" "0"
else
    record_test_result "更新版本状态" "FAIL" "0"
fi

# 测试 2.7: 级联删除
print_step "测试级联删除"
SQL_DELETE="DELETE FROM releases WHERE id = $RELEASE_ID;"

DELETE_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -c \"$SQL_DELETE\"" 2>&1)

# 验证级联删除（检查是否有孤立文件）
SQL_CHECK="SELECT COUNT(*) FROM release_files WHERE release_id = $RELEASE_ID;"

CHECK_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -t -c \"$SQL_CHECK\"" 2>&1)

ORPHAN_COUNT=$(echo "$CHECK_RESULT" | grep -oE "[0-9]+" | head -1)

if [ "$ORPHAN_COUNT" = "0" ]; then
    record_test_result "级联删除" "PASS" "0"
else
    record_test_result "级联删除" "FAIL" "0"
    print_error "存在 $ORPHAN_COUNT 个孤立文件"
fi

print_test_summary
exit $?

