#!/usr/bin/env bash
# E2E Test 04: 完整工作流测试

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_LIB="$PROJECT_ROOT/tests/lib/common.sh"

source "$TEST_LIB"

print_test_header "E2E Test 04: 完整工作流验证"

WORKFLOW_VERSION="2.4.8-e2e-flow-$(date +%s)"
WORKFLOW_DIR="/tmp/e2e-workflow-$$"
mkdir -p "$WORKFLOW_DIR/archives" "$WORKFLOW_DIR/manifests"

print_info "工作流版本: $WORKFLOW_VERSION"

# 步骤1: 模拟构建产物
print_step "步骤1: 模拟构建产物"
cd "$WORKFLOW_DIR/archives"
for os in linux darwin windows; do
    for arch in amd64 arm64; do
        if [ "$os" = "windows" ] && [ "$arch" = "arm64" ]; then
            continue
        fi
        if [ "$os" = "windows" ]; then
            suffix="zip"
        else
            suffix="tar.gz"
        fi
        echo "artifact" > "llm-gateway-go-${WORKFLOW_VERSION}-${os}-${arch}.${suffix}"
    done
done
echo "docker" > "llm-gateway-go-docker-${WORKFLOW_VERSION}.tar.gz"

FILE_COUNT=$(ls | wc -l)
if [ "$FILE_COUNT" -gt 0 ]; then
    record_test_result "步骤1-构建产物" "PASS" "0"
    print_info "产物数量: $FILE_COUNT"
else
    record_test_result "步骤1-构建产物" "FAIL" "0"
fi

# 步骤2: 生成清单
print_step "步骤2: 生成版本清单"
cd "$PROJECT_ROOT"
if bash "$PROJECT_ROOT/scripts/upload/generate-manifest.sh" "$WORKFLOW_DIR" "$WORKFLOW_VERSION" > /tmp/wf_manifest.log 2>&1; then
    record_test_result "步骤2-清单生成" "PASS" "0"
    
    if [ -f "$WORKFLOW_DIR/manifests/RELEASE-${WORKFLOW_VERSION}.json" ]; then
        JSON_FILES=$(jq '.files | length' "$WORKFLOW_DIR/manifests/RELEASE-${WORKFLOW_VERSION}.json" 2>/dev/null || echo "0")
        if [ "$JSON_FILES" -gt 0 ]; then
            record_test_result "步骤2-清单内容验证" "PASS" "0"
            print_info "清单包含 $JSON_FILES 个文件"
        else
            record_test_result "步骤2-清单内容验证" "FAIL" "0"
        fi
    fi
else
    record_test_result "步骤2-清单生成" "FAIL" "0"
fi

# 步骤3: 数据库记录
print_step "步骤3: 数据库版本记录"
DB_PASS=$(bash ~/workspace/ai-native-tools/envs/loader.sh query COMMON_PG_SUPERUSER_PASS 2>/dev/null || echo "")

if [ -z "$DB_PASS" ]; then
    skip_test_case "步骤3-数据库记录" "无法获取数据库密码"
    skip_test_case "步骤4-数据完整性" "依赖步骤3"
    skip_test_case "步骤5-清理" "依赖步骤3"
else
    SQL_INSERT="INSERT INTO releases (version, full_version, build_seq, git_sha, build_date, release_type, release_notes, status, is_public, min_postgres_version, min_redis_version, created_by) VALUES ('2.4.8', '$WORKFLOW_VERSION', 9999, 'wf123', '$(date +%Y%m%d)', 'stable', 'E2E完整工作流测试', 'draft', true, '14', '6', 'workflow-e2e') RETURNING id;"
    
    INSERT_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -t -c \"$SQL_INSERT\"" 2>&1)
    
    RELEASE_ID=$(echo "$INSERT_RESULT" | grep -oE "^[ ]*[0-9]+" | head -1 | tr -d ' ')
    
    if [ -n "$RELEASE_ID" ] && [ "$RELEASE_ID" -gt 0 ]; then
        record_test_result "步骤3-数据库记录" "PASS" "0"
        print_info "版本ID: $RELEASE_ID"
        
        # 步骤4: 验证
        print_step "步骤4: 验证数据完整性"
        
        COUNT_SQL="SELECT COUNT(*) FROM releases WHERE full_version='$WORKFLOW_VERSION';"
        COUNT_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -t -c \"$COUNT_SQL\"" 2>&1)
        
        RELEASE_CNT=$(echo "$COUNT_RESULT" | grep -oE "[0-9]+" | head -1)
        
        if [ "$RELEASE_CNT" = "1" ]; then
            record_test_result "步骤4-数据完整性" "PASS" "0"
            print_info "版本记录数: $RELEASE_CNT"
        else
            record_test_result "步骤4-数据完整性" "FAIL" "0"
        fi
        
        # 步骤5: 清理
        print_step "步骤5: 清理测试数据"
        
        CLEAN_SQL="DELETE FROM releases WHERE full_version='$WORKFLOW_VERSION';"
        CLEAN_RESULT=$(ssh -p 25022 root@8.136.114.245 "PGPASSWORD='$DB_PASS' psql -h 172.16.2.210 -p 5432 -U llm_gateway -d maintain -c \"$CLEAN_SQL\"" 2>&1)
        
        if echo "$CLEAN_RESULT" | grep -q "DELETE"; then
            record_test_result "步骤5-清理" "PASS" "0"
        else
            record_test_result "步骤5-清理" "FAIL" "0"
        fi
    else
        record_test_result "步骤3-数据库记录" "FAIL" "0"
        skip_test_case "步骤4-数据完整性" "依赖步骤3"
        skip_test_case "步骤5-清理" "依赖步骤3"
    fi
fi

# 清理本地
rm -rf "$WORKFLOW_DIR"

print_test_summary
exit $?

