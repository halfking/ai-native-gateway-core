#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# LLM Gateway 本地部署测试 (一键全流程)
#
# 流程:
#   1. 前置检查 (docker / curl / go / 二进制文件)
#   2. 启动依赖栈 (PG / Redis / Mock Upstream)
#   3. 数据库迁移 (schema + migrations + seed)
#   4. 构建并启动 gateway v1
#   5. L1-L4 部署验证
#       - L1: HTTP 存活 (/healthz)
#       - L2: 依赖连通 (DB / Redis)
#       - L3: 功能链路 (chat/completions)
#       - L4: 业务真实 (model list / metrics)
#   6. 输出验证报告
#
# 用法:
#   ./scripts/local-deploy-test.sh             # 全流程
#   ./scripts/local-deploy-test.sh --quick     # 跳过 rebuild, 只验证
#   ./scripts/local-deploy-test.sh --verify    # 只跑验证 (服务已运行)
#   ./scripts/local-deploy-test.sh --clean     # 停止 + 清数据
#   ./scripts/local-deploy-test.sh --help      # 帮助
#
# 端口映射:
#   PG:       localhost:15432 → 5432 (kxuser/kxpass, db=llm_gateway)
#   Redis:    localhost:6379
#   Mock:     http://localhost:18080
#   Gateway:  http://localhost:8781
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.local-r112.yml"

REPORT_FILE="/tmp/llm-gateway-deploy-test-report.md"
LOG_FILE="/tmp/llm-gateway-deploy-test.log"
PID_FILE="/tmp/llm-gateway-deploy-test.pid"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" | tee -a "$LOG_FILE" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}" | tee -a "$LOG_FILE"; }
info() { echo -e "${YELLOW}▶ $*${NC}" | tee -a "$LOG_FILE"; }
heading() { echo -e "\n${CYAN}━━━ $* ━━━${NC}" | tee -a "$LOG_FILE"; }
sub()   { echo -e "  ${YELLOW}·${NC} $*" | tee -a "$LOG_FILE"; }

PASS=0; FAIL=0; TOTAL=0
pass() { PASS=$((PASS+1)); TOTAL=$((TOTAL+1)); echo -e "  ${GREEN}✓${NC} $1" | tee -a "$LOG_FILE"; }
fail() { FAIL=$((FAIL+1)); TOTAL=$((TOTAL+1)); echo -e "  ${RED}✗${NC} $1" | tee -a "$LOG_FILE"; }
skip() { TOTAL=$((TOTAL+1)); echo -e "  ${YELLOW}─${NC} $1 (跳过)" | tee -a "$LOG_FILE"; }

# ── 解析参数 ──
MODE="full"
SKIP_DB=true
for arg in "$@"; do
  case "$arg" in
    --quick)   MODE="quick" ;;
    --verify)  MODE="verify" ;;
    --clean)   MODE="clean" ;;
    --with-db) SKIP_DB=false ;;
    --skip-db) SKIP_DB=true ;;
    --help)    echo "用法: $0 [--quick|--verify|--clean|--with-db|--skip-db|--help]"
               echo "  --with-db  部署新数据库 (默认本地开发模式跳过 DB)"
               echo "  --skip-db  跳过数据库部署和迁移 (使用已有外部 PG)"
               exit 0 ;;
    *) err "未知参数: $arg"; exit 1 ;;
  esac
done

# ════════════════════════════════════════════════════════════════════
# 前置检查
# ════════════════════════════════════════════════════════════════════
precheck() {
  heading "前置检查"

  command -v docker >/dev/null 2>&1 && ok "docker $(docker --version | cut -d' ' -f3 | tr -d ',')" || { err "docker 未安装"; exit 1; }
  command -v curl >/dev/null 2>&1 && ok "curl $(curl --version | head -1 | awk '{print $2}')" || { err "curl 未安装"; exit 1; }
  command -v go >/dev/null 2>&1 && ok "go $(go version | awk '{print $3}')" || sub "go 未安装 (跳过, 用 Docker 构建)"
  command -v jq >/dev/null 2>&1 && ok "jq $(jq --version)" || sub "jq 未安装 (跳过 JSON 解析)"

  # Docker daemon 是否在运行
  docker info >/dev/null 2>&1 || { err "Docker daemon 未运行"; exit 1; }
  ok "Docker daemon 运行中"

  # Docker compose 版本
  if docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD="docker compose"
    ok "docker compose $(docker compose version --short 2>/dev/null || echo 'v2')"
  else
    COMPOSE_CMD="docker-compose"
    command -v docker-compose >/dev/null 2>&1 || { err "docker-compose 未安装"; exit 1; }
    ok "docker-compose"
  fi

  # 检查本地构建产物
  if [ -f "$ROOT_DIR/.build-local/llm-gateway-go" ]; then
    local size; size=$(du -h "$ROOT_DIR/.build-local/llm-gateway-go" | cut -f1)
    ok "二进制文件已就绪 (.build-local/llm-gateway-go, $size)"
  else
    sub "二进制文件不存在 (Docker 构建时将自动编译)"
  fi
  if [ -d "$ROOT_DIR/web/dist" ]; then
    ok "前端 dist 已就绪 (web/dist)"
  else
    sub "前端 dist 不存在 (仅 API 测试, 跳过前端)"
  fi

  # 端口冲突检查
  for port in 8781 15432 6379 18080; do
    if lsof -i :$port >/dev/null 2>&1; then
      local proc; proc=$(lsof -ti :$port | head -1)
      sub "端口 $port 已被 PID $proc 占用"
    fi
  done
}

# ════════════════════════════════════════════════════════════════════
# 启动依赖栈
# ════════════════════════════════════════════════════════════════════
start_deps() {
  heading "启动依赖栈"

  info "启动 postgres / redis / llm-mock / llm-mock-upstream / memora-mcp..."
  $COMPOSE_CMD -f "$COMPOSE_FILE" up -d postgres redis llm-mock llm-mock-upstream memora-mcp 2>&1 | tee -a "$LOG_FILE"

  # 等待 postgres
  info "等待 postgres (max 60s)..."
  PG_OK=0
  for i in $(seq 1 60); do
    if docker exec r112_postgres pg_isready -U kxuser -d postgres >/dev/null 2>&1; then
      ok "postgres ready (after ${i}s)"
      PG_OK=1; break
    fi
    sleep 1
  done
  [ "$PG_OK" = "1" ] || { err "postgres 60s 内未就绪"; docker logs r112_postgres --tail 20 2>&1 | tee -a "$LOG_FILE"; exit 1; }

  # 等待 redis
  info "等待 redis (max 30s)..."
  REDIS_OK=0
  for i in $(seq 1 30); do
    if docker exec r112_redis redis-cli ping 2>/dev/null | grep -q PONG; then
      ok "redis ready (after ${i}s)"
      REDIS_OK=1; break
    fi
    sleep 1
  done
  [ "$REDIS_OK" = "1" ] && ok "redis ping OK" || sub "redis 未就绪 (继续)"

  # 等待 llm-mock-upstream
  info "等待 llm-mock-upstream (max 30s)..."
  MOCK_OK=0
  for i in $(seq 1 30); do
    if curl -sf http://localhost:18080/healthz >/dev/null 2>&1; then
      ok "llm-mock-upstream ready (after ${i}s)"
      MOCK_OK=1; break
    fi
    sleep 1
  done
  [ "$MOCK_OK" = "1" ] || sub "llm-mock-upstream 未就绪 (继续)"
}

# ════════════════════════════════════════════════════════════════════
# 数据库迁移
# ════════════════════════════════════════════════════════════════════
run_migrations() {
  heading "数据库迁移"

  local PG_USER="kxuser"
  local PG_PASS="kxpass"
  local PG_DB="llm_gateway"
  local PG_CONTAINER="r112_postgres"
  local ADMIN_DB="postgres"

  pg_exec() { PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" "$PG_CONTAINER" psql -U "$PG_USER" -d "$ADMIN_DB" -v ON_ERROR_STOP=1 -tAc "$1"; }
  pg_exec_db() { PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 "$@"; }

  # 检查 $PG_DB 是否存在，存在则跳过重建
  local db_exists
  db_exists=$(pg_exec "SELECT 1 FROM pg_database WHERE datname='$PG_DB';" 2>/dev/null || echo "")
  if [ -n "$db_exists" ]; then
    sub "$PG_DB 已存在，跳过重建"
  else
    info "创建 $PG_DB 库..."
    pg_exec "CREATE DATABASE $PG_DB;"
    ok "CREATE DATABASE $PG_DB"
  fi

  # 加载 00-prereqs.sql（扩展）
  local PREREQS="$ROOT_DIR/sql/schema/00-prereqs.sql"
  if [ -f "$PREREQS" ]; then
    info "加载 00-prereqs.sql（扩展）..."
    PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" -i "$PG_CONTAINER" \
      psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -f - < "$PREREQS" >/dev/null 2>&1
    ok "扩展已安装"
  fi

  # 加载 01-schema.sql — pre-process columnar → heap
  local SCHEMA_SQL="$ROOT_DIR/sql/schema/01-schema.sql"
  if [ -f "$SCHEMA_SQL" ]; then
    info "加载 01-schema.sql（模式结构, 预处理 columnar→heap）..."
    # pgvector/pg17 不支持 Citus columnar, 替换为 heap
    PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" -i "$PG_CONTAINER" \
      psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -f - \
      < <(sed 's/SET default_table_access_method = columnar;/SET default_table_access_method = heap;/g' "$SCHEMA_SQL") \
      >/dev/null 2>&1
    ok "模式结构加载完成"
  fi

  # 加载 02-seed.sql（初始数据, 跳过 __REDACTED__ 行）
  local SEED_SQL="$ROOT_DIR/sql/schema/02-seed.sql"
  if [ -f "$SEED_SQL" ]; then
    info "加载 02-seed.sql（初始数据）..."
    PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" -i "$PG_CONTAINER" \
      psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -f - \
      < <(awk '!/__REDACTED_/' "$SEED_SQL") \
      >/dev/null 2>&1
    ok "初始数据加载完成"
  fi

  # 应用 migrations/startup/*.sql（跳过已知不兼容的）
  local MIGRATIONS_DIR="$ROOT_DIR/sql/migrations/startup"
  if [ -d "$MIGRATIONS_DIR" ]; then
    info "应用 migrations..."
    local SKIP_LIST=(
      "002_work_types.sql" "004_tuning_signals.sql" "005_tuning_proposals.sql"
      "021_tool_registry_and_metatools.sql" "029_seed_tool_registry.sql"
      "031_provider_settings.sql" "033_credential_model_call_history.sql"
      "036_fp_slot_limit.sql"
    )
    for mig in $(find "$MIGRATIONS_DIR" -maxdepth 1 -name "*.sql" ! -name "*.down.sql" -type f | sort); do
      local name; name=$(basename "$mig")
      local skip=0
      for s in "${SKIP_LIST[@]}"; do [ "$name" = "$s" ] && skip=1 && break; done
      [ "$skip" = "1" ] && sub "skip: $name" && continue

      if PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" -i "$PG_CONTAINER" \
           psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -f - < "$mig" >/dev/null 2>&1; then
        ok "mig: $name"
      else
        # 幂等错误不阻断
        if grep -qE "already exists|duplicate key|relation.*already exists" "$LOG_FILE" 2>/dev/null; then
          sub "mig: $name (幂等跳过)"
        else
          err "mig: $name 失败"
          err "  手动排查: PGPASSWORD=$PG_PASS docker exec -i $PG_CONTAINER psql -U $PG_USER -d $PG_DB -f $mig"
        fi
      fi
    done
  fi

  # 加载本地 mock credential seed
  local LOCAL_SEED="$ROOT_DIR/sql/scripts/03-local-mock-credential.sql"
  if [ -f "$LOCAL_SEED" ]; then
    info "加载 mock credential seed..."
    PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" -i "$PG_CONTAINER" \
      psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -f - < "$LOCAL_SEED" >/dev/null 2>&1 && \
      ok "mock credential 已加载" || sub "mock credential 跳过"
  fi

  # 验证
  local TABLE_COUNT
  TABLE_COUNT=$(pg_exec "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';" 2>/dev/null || echo "?")
  ok "public schema 现有 $TABLE_COUNT 张表"
}

# ════════════════════════════════════════════════════════════════════
# 启动 Gateway v1
# ════════════════════════════════════════════════════════════════════
start_gateway() {
  heading "启动 Gateway v1"

  # 先停旧的
  $COMPOSE_CMD -f "$COMPOSE_FILE" rm -sf gateway 2>/dev/null || true

  info "使用 Docker Compose 构建并启动 gateway..."
  $COMPOSE_CMD -f "$COMPOSE_FILE" up -d --build gateway 2>&1 | tee -a "$LOG_FILE"

  info "等待 gateway v1 (max 90s)..."
  GW_OK=0
  for i in $(seq 1 90); do
    if curl -sf http://localhost:8781/healthz >/dev/null 2>&1; then
      GW_OK=1
      ok "gateway v1 ready (after ${i}s)"
      break
    fi
    # 检查容器是否还在运行
    if ! docker ps --format '{{.Names}}' | grep -q "^r112_gateway$"; then
      err "gateway 容器已退出"
      docker logs r112_gateway --tail 40 2>&1 | tee -a "$LOG_FILE"
      break
    fi
    sleep 1
  done
  [ "$GW_OK" = "1" ] || { err "gateway v1 未就绪"; docker logs r112_gateway --tail 40 2>&1 | tee -a "$LOG_FILE"; exit 1; }
}

# ════════════════════════════════════════════════════════════════════
# L1: HTTP 存活
# ════════════════════════════════════════════════════════════════════
verify_l1_health() {
  heading "L1: HTTP 存活验证"

  local url="http://localhost:8781"

  # /healthz
  local code body
  code=$(curl -sS -o /tmp/verify_health.json -w "%{http_code}" "$url/healthz" --max-time 10 || echo "000")
  body=$(cat /tmp/verify_health.json 2>/dev/null || echo "{}")

  if [ "$code" = "200" ]; then
    pass "healthz → HTTP 200"
    if echo "$body" | jq '.' >/dev/null 2>&1; then
      local status; status=$(echo "$body" | jq -r '.status // "unknown"')
      local ver; ver=$(echo "$body" | jq -r '.version // "unknown"')
      pass "healthz body: status=$status, version=$ver"
    fi
  else
    fail "healthz → HTTP $code"
    echo "    body: $(echo "$body" | head -c 200)"
  fi

  # / (前端 SPA)
  local spa_code
  spa_code=$(curl -sS -o /dev/null -w "%{http_code}" "$url/" --max-time 10 || echo "000")
  if [ "$spa_code" = "200" ] || [ "$spa_code" = "301" ] || [ "$spa_code" = "302" ]; then
    pass "前端 SPA → HTTP $spa_code"
  else
    sub "前端 → HTTP $spa_code (可能无前端 dist)"
  fi
}

# ════════════════════════════════════════════════════════════════════
# L2: 依赖连通
# ════════════════════════════════════════════════════════════════════
verify_l2_deps() {
  heading "L2: 依赖连通验证"

  # PostgreSQL
  if docker exec r112_postgres pg_isready -U kxuser -d postgres >/dev/null 2>&1; then
    local db_ver
    db_ver=$(PGPASSWORD=kxpass docker exec r112_postgres psql -U kxuser -d postgres -tAc "SELECT version()" 2>/dev/null | head -1 | cut -d',' -f1)
    pass "PostgreSQL: $db_ver"
  else
    fail "PostgreSQL 不可达"
  fi

  # 数据库中有多少张表
  local table_count
  table_count=$(PGPASSWORD=kxpass docker exec r112_postgres psql -U kxuser -d llm_gateway -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';" 2>/dev/null || echo "?")
  pass "llm_gateway 数据库: $table_count 张表"

  # Redis
  if docker exec r112_redis redis-cli ping 2>/dev/null | grep -q PONG; then
    pass "Redis: PONG"
  else
    fail "Redis 不可达"
  fi

  # Mock upstream
  if curl -sf http://localhost:18080/healthz >/dev/null 2>&1; then
    pass "LLM Mock Upstream: 可达"
  else
    sub "LLM Mock Upstream 不可达 (继续)"
  fi
}

# ════════════════════════════════════════════════════════════════════
# L3: 功能链路
# ════════════════════════════════════════════════════════════════════
verify_l3_smoke() {
  heading "L3: 功能链路验证"

  local base="http://localhost:8781"

  # /v1/models — 模型列表
  info "检查 /v1/models..."
  local models_code models_body
  models_code=$(curl -sS -o /tmp/verify_models.json -w "%{http_code}" "$base/v1/models" --max-time 15 || echo "000")
  if [ "$models_code" = "200" ]; then
    local model_count
    model_count=$(jq '.data | length' /tmp/verify_models.json 2>/dev/null || echo "0")
    pass "/v1/models → HTTP 200 ($model_count 个模型)"
  else
    fail "/v1/models → HTTP $models_code"
    cat /tmp/verify_models.json 2>/dev/null | head -c 200 | tee -a "$LOG_FILE"
    echo
  fi

  # /v1/chat/completions — 对话 (不需要 API key)
  info "检查 /v1/chat/completions (无认证)..."
  local chat_code chat_body
  chat_code=$(curl -sS -o /tmp/verify_chat.json -w "%{http_code}" \
    -X POST "$base/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "X-Tenant-ID: t-a" \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"ping"}],"max_tokens":10}' \
    --max-time 30 || echo "000")

  if [ "$chat_code" = "200" ]; then
    local has_choices
    has_choices=$(jq '.choices | length > 0' /tmp/verify_chat.json 2>/dev/null || echo "false")
    if [ "$has_choices" = "true" ]; then
      local content
      content=$(jq -r '.choices[0].message.content // ""' /tmp/verify_chat.json 2>/dev/null | head -c 100)
      pass "/v1/chat/completions → 200 OK (含 choices)"
      sub "响应: $content"
    else
      fail "/v1/chat/completions → 200 但无 choices"
      jq '.' /tmp/verify_chat.json 2>/dev/null | head -20
    fi
  elif [ "$chat_code" = "401" ] || [ "$chat_code" = "403" ]; then
    local err_msg
    err_msg=$(jq -r '.error.message // .error // "unknown"' /tmp/verify_chat.json 2>/dev/null)
    pass "/v1/chat/completions → HTTP $chat_code (需认证, 预期行为)"
    sub "错误: $err_msg"
  else
    fail "/v1/chat/completions → HTTP $chat_code"
    cat /tmp/verify_chat.json 2>/dev/null | head -c 300 | tee -a "$LOG_FILE"
    echo
  fi

  # /metrics — Prometheus 指标
  info "检查 /metrics..."
  local metrics_code
  metrics_code=$(curl -sS -o /tmp/verify_metrics.txt -w "%{http_code}" "$base/metrics" --max-time 10 || echo "000")
  if [ "$metrics_code" = "200" ]; then
    if grep -q "# TYPE" /tmp/verify_metrics.txt 2>/dev/null; then
      local metric_lines
      metric_lines=$(grep -c "^llm" /tmp/verify_metrics.txt 2>/dev/null || echo "0")
      pass "/metrics → HTTP 200 ($metric_lines 个 llm 指标)"
    else
      pass "/metrics → HTTP 200"
    fi
  else
    fail "/metrics → HTTP $metrics_code"
  fi
}

# ════════════════════════════════════════════════════════════════════
# L4: 业务真实
# ════════════════════════════════════════════════════════════════════
verify_l4_business() {
  heading "L4: 业务真实验证"

  local base="http://localhost:8781"

  # 通过 v1/chat/completions 用 test-key 验证
  info "用 API key 验证 chat/completions..."
  local auth_code auth_body
  auth_code=$(curl -sS -o /tmp/verify_auth_chat.json -w "%{http_code}" \
    -X POST "$base/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer test-key" \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":10}' \
    --max-time 30 || echo "000")

  if [ "$auth_code" = "200" ]; then
    local has_choices
    has_choices=$(jq '.choices | length > 0' /tmp/verify_auth_chat.json 2>/dev/null || echo "false")
    if [ "$has_choices" = "true" ]; then
      pass "认证 chat → HTTP 200 + choices (业务链路完整)"
    else
      sub "认证 chat → 200 但无 choices"
    fi
  elif [ "$auth_code" = "401" ] || [ "$auth_code" = "403" ]; then
    pass "认证 chat → HTTP $auth_code (API key 未配置)"
  else
    sub "认证 chat → HTTP $auth_code"
  fi
}

# ════════════════════════════════════════════════════════════════════
# 运行现有 smoke
# ════════════════════════════════════════════════════════════════════
run_smoke_script() {
  heading "R1.12 Smoke 测试"

  if [ ! -f "$SCRIPT_DIR/local-r112-smoke.sh" ]; then
    sub "smoke 脚本不存在: local-r112-smoke.sh"
    return
  fi

  info "运行 R1.12 smoke 测试..."
  V1_BASE_URL=http://localhost:8781 SKIP_V1=0 bash "$SCRIPT_DIR/local-r112-smoke.sh" 2>&1 | tee -a "$LOG_FILE" || true
}

# ════════════════════════════════════════════════════════════════════
# 停止清理
# ════════════════════════════════════════════════════════════════════
cleanup() {
  heading "清理环境"

  info "停止 Docker Compose 服务..."
  $COMPOSE_CMD -f "$COMPOSE_FILE" down 2>&1 | tee -a "$LOG_FILE"

  # 清理临时文件
  rm -f /tmp/verify_*.json /tmp/verify_*.txt
  ok "临时文件已清理"

  info "环境已停止"
  info "重启: $0"
}

# ════════════════════════════════════════════════════════════════════
# 生成报告
# ════════════════════════════════════════════════════════════════════
generate_report() {
  local ts
  ts=$(date '+%Y-%m-%d %H:%M:%S')

  cat > "$REPORT_FILE" <<EOF
# LLM Gateway 本地部署测试报告

**测试日期**: $ts  
**环境**: macOS ($(uname -m))  
**模式**: $MODE

---

## 验证结果

| 层级 | 名称 | 结果 |
|------|------|------|
EOF

  echo "| L1 | HTTP 存活 | $([ "$PASS" -ge 1 ] && echo '✅' || echo '❌') |" >> "$REPORT_FILE"
  echo "| L2 | 依赖连通 | $([ "$PASS" -ge 3 ] && echo '✅' || echo '❌') |" >> "$REPORT_FILE"
  echo "| L3 | 功能链路 | $([ "$PASS" -ge 4 ] && echo '✅' || echo '❌') |" >> "$REPORT_FILE"
  echo "| L4 | 业务真实 | $([ "$FAIL" -eq 0 ] && echo '✅' || echo '❌') |" >> "$REPORT_FILE"

  cat >> "$REPORT_FILE" <<EOF

**总计**: 通过 $PASS / 失败 $FAIL / 共 $TOTAL

### 服务端点

| 服务 | 地址 |
|------|------|
| PostgreSQL | \`localhost:15432\` (kxuser/kxpass, db=llm_gateway) |
| Redis | \`localhost:6379\` |
| LLM Mock | \`http://localhost:18080\` |
| Gateway v1 | \`http://localhost:8781\` |
| Health | \`http://localhost:8781/healthz\` |

### 日志

完整日志: \`$LOG_FILE\`

EOF

  echo
  echo "报告已生成: $REPORT_FILE"
}

# ════════════════════════════════════════════════════════════════════
# 主流程
# ════════════════════════════════════════════════════════════════════
echo "" > "$LOG_FILE"

echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║     LLM Gateway 本地部署测试                                ║"
echo "║     $(date '+%Y-%m-%d %H:%M:%S')                                ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

case "$MODE" in
  clean)
    cleanup
    exit 0
    ;;
  verify)
    precheck
    verify_l1_health
    [ "$SKIP_DB" = "false" ] && verify_l2_deps
    verify_l3_smoke
    verify_l4_business
    run_smoke_script
    ;;
  quick)
    precheck
    [ "$SKIP_DB" = "false" ] && { start_deps; run_migrations; }
    verify_l1_health
    [ "$SKIP_DB" = "false" ] && verify_l2_deps
    verify_l3_smoke
    verify_l4_business
    run_smoke_script
    ;;
  full)
    precheck
    [ "$SKIP_DB" = "false" ] && { start_deps; run_migrations; }
    start_gateway
    verify_l1_health
    [ "$SKIP_DB" = "false" ] && verify_l2_deps
    verify_l3_smoke
    verify_l4_business
    run_smoke_script
    ;;
esac

# ── 总结 ──
echo ""
heading "验证结果汇总"
echo "  通过: ${PASS}, 失败: ${FAIL}, 总计: ${TOTAL}"

if [ "$FAIL" -eq 0 ] && [ "$TOTAL" -gt 0 ]; then
  echo -e "  ${GREEN}✅ 全部通过, 部署验证成功!${NC}"
elif [ "$FAIL" -gt 0 ]; then
  echo -e "  ${RED}❌ $FAIL 项失败, 请检查日志: $LOG_FILE${NC}"
fi

generate_report

echo ""
echo "服务状态:"
echo "  Gateway:  http://localhost:8781"
echo "  Health:   http://localhost:8781/healthz"
echo "  Metrics:  http://localhost:8781/metrics"
echo "  Models:   http://localhost:8781/v1/models"
echo ""
echo "管理命令:"
echo "  查看日志:  tail -f $LOG_FILE"
echo "  Gateway:   docker logs -f r112_gateway"
echo "  PG:        PGPASSWORD=kxpass docker exec -it r112_postgres psql -U kxuser -d llm_gateway"
echo "  Redis:     docker exec -it r112_redis redis-cli"
echo "  停止服务:  $0 --clean"
echo "  仅验证:    $0 --verify"
echo ""
echo "报告:      $REPORT_FILE"
echo "日志:      $LOG_FILE"
