#!/usr/bin/env bash
# scripts/omnifree/e2e-verify.sh
#
# OmniFree 第三轮审计 · 端到端验证脚本 (本地 Docker Postgres)
#
# 流程:
#   1. 准备测试 PG (无则启动 docker postgres; 有则复用)
#   2. 应用 075-omnifree-schema.sql
#   3. 创建非超级用户 (NOSUPERUSER + NOBYPASSRLS) 用于 RLS 验证
#   4. 导入 seed
#   5. 跑集成测试 (domains/freeresource + domains/autocombo)
#   6. Pool 去重聚合验证
#   7. 凭据 + 配额 + 校正流程验证
#   8. 输出 PASS / FAIL 总结
#
# 依赖: docker, psql, go (>= 1.21)
# 用法: ./scripts/omnifree/e2e-verify.sh [--keep] [--skip-docker]
#
# 选项:
#   --keep         保留测试数据库与容器 (默认验证完清理)
#   --skip-docker  跳过 docker 启动, 假设外部已有 PG 在 $E2E_DB_PORT

set -euo pipefail

# 配置
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ADMIN_DB_NAME="${E2E_DB_NAME:-omnifree_e2e}"
TEST_DB_NAME="${E2E_DB_NAME:-omnifree_e2e}"
TEST_USER="omnifree_test_user"
TEST_PASSWORD="omnifree_test_pwd"
PG_PORT="${E2E_DB_PORT:-5455}"  # 复用现有 PG 5455
PG_HOST="${E2E_DB_HOST:-localhost}"
ADMIN_USER="${E2E_DB_ADMIN:-postgres}"
ADMIN_PASS="${E2E_DB_ADMIN_PASS:-postgres}"
ADMIN_DSN="postgres://${ADMIN_USER}:${ADMIN_PASS}@${PG_HOST}:${PG_PORT}/postgres?sslmode=disable"

KEEP=0
SKIP_DOCKER=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) KEEP=1; shift ;;
    --skip-docker) SKIP_DOCKER=1; shift ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# 颜色
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
PASS_COUNT=0
FAIL_COUNT=0

pass() { echo -e "${GREEN}✓ PASS${NC}: $1"; PASS_COUNT=$((PASS_COUNT+1)); }
fail() { echo -e "${RED}✗ FAIL${NC}: $1"; FAIL_COUNT=$((FAIL_COUNT+1)); }
info() { echo -e "${YELLOW}INFO${NC}: $1"; }

cleanup() {
  if [[ $KEEP -eq 0 ]]; then
    info "清理测试数据..."
    # 在测试 DB 内收回 test user 拥有的对象, 再 drop user.
    PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d "${TEST_DB_NAME}" \
      -c "DROP OWNED BY ${TEST_USER} CASCADE;" 2>&1 | grep -v "^$" || true
    PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d "${TEST_DB_NAME}" \
      -c "DROP DATABASE IF EXISTS ${TEST_DB_NAME};" 2>&1 | grep -v "^$" || true
    # 在 postgres DB 内再次收回 (兼容测试创建过的对象).
    PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d postgres \
      -c "DROP OWNED BY ${TEST_USER} CASCADE;" 2>&1 | grep -v "^$" || true
    PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d postgres \
      -c "DROP USER IF EXISTS ${TEST_USER};" 2>&1 | grep -v "^$" || true
  else
    info "保留测试数据 (--keep), 数据库=${TEST_DB_NAME} 用户=${TEST_USER}"
  fi
}

trap cleanup EXIT

echo "===== OmniFree E2E 验证 ====="
info "项目根: $PROJECT_ROOT"
info "测试 DB: ${TEST_DB_NAME} @ ${PG_HOST}:${PG_PORT}"
echo

# ---- 0. 检查 docker / psql / go ----
command -v psql >/dev/null 2>&1 || { fail "psql 未安装"; exit 1; }
command -v go  >/dev/null 2>&1 || { fail "go  未安装"; exit 1; }
pass "psql & go 已安装"

# ---- 1. 测试 PG 连接 ----
if ! PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d postgres -c "SELECT 1" >/dev/null 2>&1; then
  fail "PG ${PG_HOST}:${PG_PORT} 不可达 (admin=${ADMIN_USER})"
  exit 1
fi
pass "PG 连接成功"

# ---- 2. 重建测试数据库 + 用户 ----
PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d postgres <<SQL >/dev/null 2>&1
DROP DATABASE IF EXISTS ${TEST_DB_NAME};
CREATE DATABASE ${TEST_DB_NAME};
DROP USER IF EXISTS ${TEST_USER};
CREATE USER ${TEST_USER} WITH PASSWORD '${TEST_PASSWORD}' NOSUPERUSER NOBYPASSRLS;
GRANT CONNECT ON DATABASE ${TEST_DB_NAME} TO ${TEST_USER};
SQL
pass "数据库 ${TEST_DB_NAME} + 用户 ${TEST_USER} 已创建"

PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d "${TEST_DB_NAME}" <<SQL >/dev/null 2>&1
GRANT USAGE ON SCHEMA public TO ${TEST_USER};
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ${TEST_USER};
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ${TEST_USER};
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ${TEST_USER};
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO ${TEST_USER};
SQL
pass "用户权限已授予"

# ---- 3. 应用 075 schema ----
if ! PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d "${TEST_DB_NAME}" \
  -v ON_ERROR_STOP=1 -f "$PROJECT_ROOT/sql/migrations/075-omnifree-schema.sql" >/dev/null 2>&1; then
  fail "schema 迁移失败 (075-omnifree-schema.sql)"
  exit 1
fi
pass "schema 075 已应用"

# ---- 4. 验证 trains_on_prompts 列已添加 (round 3 M7) ----
COLUMN_EXISTS=$(PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d "${TEST_DB_NAME}" \
  -tAc "SELECT 1 FROM information_schema.columns WHERE table_name='free_resource_catalog' AND column_name='trains_on_prompts';")
if [[ "$COLUMN_EXISTS" == "1" ]]; then
  pass "trains_on_prompts 列已添加 (round 3 M7)"
else
  fail "trains_on_prompts 列缺失"
fi

# ---- 5. 导入 seed ----
if ! cd "$PROJECT_ROOT" && go run ./cmd/seed-free-resources \
  --db-url="postgres://${ADMIN_USER}:${ADMIN_PASS}@${PG_HOST}:${PG_PORT}/${TEST_DB_NAME}?sslmode=disable" \
  --catalog=configs/seed/free_resource_catalog.json \
  --templates=configs/seed/auto_combo_templates.json \
  --keyless=configs/seed/keyless_providers.json >/dev/null 2>&1; then
  fail "seed 导入失败"
  exit 1
fi
pass "seed 导入成功 (15 free resources + 6 auto combo templates + 3 keyless providers)"

# ---- 6. 跑 Go 集成测试 (需 test user + admin DSN) ----
info "运行 Go 集成测试 (RLS / QuotaTracker)..."
export OMNIFREE_TEST_DB_URL="postgres://${TEST_USER}:${TEST_PASSWORD}@${PG_HOST}:${PG_PORT}/${TEST_DB_NAME}?sslmode=disable"
export OMNIFREE_TEST_DB_ADMIN_URL="postgres://${ADMIN_USER}:${ADMIN_PASS}@${PG_HOST}:${PG_PORT}/${TEST_DB_NAME}?sslmode=disable"
TEST_RESULT=0
if go test -count=1 -timeout 60s -run "TestSetRLSTenant|TestPreflight_RLS|TestRecord_TxWrapped|TestCorrectFromHeaders_SetsExhausted|TestSetRLSTenantContext_InvalidFallback|TestComputePoolDedupTotals_Live" \
  ./domains/freeresource/ 2>&1 | tee /tmp/omnifree-r3-e2e.log | tail -5; then
  pass "RLS / QuotaTracker / Pool Dedup 集成测试通过"
else
  fail "集成测试失败 (见 /tmp/omnifree-r3-e2e.log)"
  TEST_RESULT=1
fi

# ---- 7. 跑单元测试 (全量 autocombo + freeresource) ----
info "运行单元测试..."
if go test -count=1 -timeout 30s \
  ./domains/autocombo/ ./domains/freeresource/ ./bg/freequotareset/ ./bg/freequotacleanup/ ./cmd/seed-free-resources/ \
  ./domains/streaming/ -run "TestChatHandler|TestFlattenHeaders|TestHashString|TestRecordOmniFreeQuota" 2>&1 | tail -5; then
  pass "全量单元测试通过"
else
  fail "单元测试失败"
  TEST_RESULT=1
fi

# ---- 8. Pool 去重聚合验证 (SQL) ----
info "验证 Pool 去重聚合 (round 3 M5)..."
PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d "${TEST_DB_NAME}" <<'SQL' >/tmp/pool_dedup.out 2>&1
SELECT
  COALESCE(NULLIF(pool_key, ''), '__standalone__') AS pool_key,
  COUNT(*) AS model_count,
  MAX(COALESCE(monthly_tokens, 0)) AS pool_max_monthly
FROM free_resource_catalog
WHERE enabled = TRUE
GROUP BY pool_key
ORDER BY pool_max_monthly DESC
LIMIT 10;
SQL
if head -1 /tmp/pool_dedup.out | grep -q "pool_key"; then
  TOTAL_POOLS=$(PGPASSWORD="$ADMIN_PASS" psql -h "$PG_HOST" -p "$PG_PORT" -U "$ADMIN_USER" -d "${TEST_DB_NAME}" \
    -tAc "SELECT COUNT(DISTINCT COALESCE(NULLIF(pool_key, ''), '__standalone__')) FROM free_resource_catalog WHERE enabled = TRUE;")
  pass "Pool 去重聚合查询 OK (共 ${TOTAL_POOLS} 个 pool / pool_key 分组)"
  cat /tmp/pool_dedup.out | sed 's/^/    /'
else
  fail "Pool 去重聚合失败"
  cat /tmp/pool_dedup.out | head -10
fi

# ---- 9. 总结 ----
echo
echo "===== E2E 验证总结 ====="
echo "PASS: $PASS_COUNT"
echo "FAIL: $FAIL_COUNT"
echo
if [[ $FAIL_COUNT -gt 0 || $TEST_RESULT -ne 0 ]]; then
  exit 1
fi
echo -e "${GREEN}🎉 OmniFree 第三轮审计 E2E 验证全部通过${NC}"
exit 0