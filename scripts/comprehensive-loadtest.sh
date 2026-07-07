#!/usr/bin/env bash
# ====================================================================
# 完整的本地部署和压力测试方案
# ====================================================================
# 功能:
#   - 启动 10+ mock 供应商实例 (支持多模型)
#   - 启动 gateway
#   - 注入 credentials 到数据库
#   - 运行 20+ 客户端并发压力测试
#   - 测试多种故障场景 (rate_limit, slow, server_error, flaky)
#   - 验证 sticky routing, failover, 多轮会话
# ====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 配置 ──
NUM_MOCK_PROVIDERS=12        # 12 个 mock 供应商
NUM_CLIENTS=25               # 25 个并发客户端
NUM_ROUNDS_PER_CLIENT=100    # 每客户端 100 轮请求
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-xutaohuang}"
DB_PASSWORD="${DB_PASSWORD:-}"

# Mock 端口范围: 19080-19091 (12个)
MOCK_START_PORT=19080

# ── 颜色输出 ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

# ── 检查依赖 ──
check_dependencies() {
    log_info "检查依赖..."
    
    local missing=()
    
    command -v python3 >/dev/null 2>&1 || missing+=("python3")
    command -v psql >/dev/null 2>&1 || missing+=("psql")
    command -v curl >/dev/null 2>&1 || missing+=("curl")
    command -v jq >/dev/null 2>&1 || missing+=("jq")
    command -v go >/dev/null 2>&1 || missing+=("go")
    
    if [[ ${#missing[@]} -gt 0 ]]; then
        log_error "缺少依赖: ${missing[*]}"
        exit 1
    fi
    
    # 检查 Python 依赖
    python3 -c "import aiohttp" 2>/dev/null || {
        log_error "缺少 Python 模块: aiohttp (运行: pip3 install aiohttp)"
        exit 1
    }
    
    log_success "依赖检查通过"
}

# ── 编译 gateway ──
build_gateway() {
    log_info "编译 gateway (整包编译 ./cmd/gateway/)..."
    cd "$ROOT_DIR"
    
    # 整包编译：cmd/gateway 是由多个 .go 文件组成的 package main，
    # 必须用包路径编译，单文件编译会报 undefined。
    go build -o gateway ./cmd/gateway/
    log_success "gateway 编译完成"
}

# ── 启动 mock 供应商 ──
start_mock_providers() {
    log_info "启动 $NUM_MOCK_PROVIDERS 个 mock 供应商实例..."
    
    cd "$SCRIPT_DIR/mocks/llm-mock-upstream"
    
    local started=0
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        local token=$(printf "mock-%02d" $i)
        
        # 检查端口是否已被占用
        if curl -sS --max-time 1 "http://localhost:$port/healthz" > /dev/null 2>&1; then
            log_info "  mock-$token (port $port) 已在运行"
            started=$((started + 1))
            continue
        fi
        
        # 启动 mock 进程
        MOCK_PORT=$port \
        MOCK_TOKEN=$token \
        MOCK_STATE_FILE="/tmp/mock-state-$port.json" \
        python3 server-v2.py > "/tmp/mock-$port.log" 2>&1 &
        
        local pid=$!
        echo $pid > "/tmp/mock-$port.pid"
        log_success "  启动 $token (port $port, PID $pid)"
        started=$((started + 1))
    done
    
    # 等待所有 mock 启动
    sleep 3
    
    log_info "验证 mock 可用性..."
    local healthy=0
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        if curl -sS --max-time 2 "http://localhost:$port/healthz" > /dev/null 2>&1; then
            healthy=$((healthy + 1))
        else
            log_warn "  mock-$(printf "%02d" $i) (port $port) 未响应"
        fi
    done
    
    log_success "$healthy/$NUM_MOCK_PROVIDERS 个 mock 供应商可用"
    
    if [[ $healthy -lt $((NUM_MOCK_PROVIDERS / 2)) ]]; then
        log_error "超过一半的 mock 不可用，中止测试"
        exit 1
    fi
}

# ── 注入 credentials 到数据库 ──
inject_credentials() {
    log_info "注入 $NUM_MOCK_PROVIDERS 个 mock credentials 到数据库..."
    
    local sql_file="/tmp/loadtest-comprehensive-credentials.sql"
    
    cat > "$sql_file" <<'EOSQL'
-- ====================================================================
-- Comprehensive Loadtest Mock Credentials
-- ====================================================================
BEGIN;

-- 清理旧数据
DELETE FROM public.credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9099;
DELETE FROM public.provider_models WHERE provider_id BETWEEN 9010 AND 9099;
DELETE FROM public.credentials WHERE id BETWEEN 9010 AND 9099;
DELETE FROM public.providers WHERE id BETWEEN 9010 AND 9099;

EOSQL

    # 生成 providers, provider_models, credentials, bindings
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local provider_id=$((9010 + i))
        local port=$((MOCK_START_PORT + i))
        local token=$(printf "mock-%02d" $i)
        local code="loadtest-mock-$(printf "%02d" $i)"
        local label="Loadtest Mock $(printf "%02d" $i)"
        
        cat >> "$sql_file" <<EOSQL

-- Provider $token (localhost:$port)
-- 列严格匹配实际 DB schema（providers 表无 egress_profile/domestic 列）
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, enabled, manual_disabled
) VALUES (
    $provider_id, 'default', '$code', '$label',
    'cloud', 'official', 'openai-completions',
    'http://localhost:$port', true, false
);

-- Provider Models（provider_models 表只有 5 列：id, provider_id, raw_model_name, outbound_model_name, created_at）
INSERT INTO public.provider_models (
    id, provider_id, raw_model_name, outbound_model_name
) VALUES
    ($((provider_id * 10)), $provider_id, 'gpt-4o', 'gpt-4o'),
    ($((provider_id * 10 + 1)), $provider_id, 'gpt-4o-mini', 'gpt-4o-mini');

-- Credential（schema重建后列齐全；fp_slot_limit<=concurrency_limit 满足 CHECK）
-- ciphertext 用 LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=AAAA...(项目测试key) 加密
-- 由 secret.EncryptAESGCM 生成，与 comprehensive-loadtest.sh start_gateway 的 key 一致
INSERT INTO public.credentials (
    id, provider_id, tenant_id, label, secret_ciphertext,
    status, lifecycle_status, availability_state,
    quota_state, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
) VALUES (
    $provider_id, $provider_id, 'default', '$code-key',
    'v1:legacy:vjWiHSUIEOxTTk1sajTWejxfYsaGnu18MMcag5N5Mebvy6WQ8ewAtOyVcuo',
    'active', 'active', 'ready',
    'ok', 'closed', false, 50, 100
);

-- Credential Model Bindings（无 routing_tier/weight/manual_priority 列）
INSERT INTO public.credential_model_bindings (
    id, credential_id, provider_model_id, available
) VALUES
    ($((provider_id * 10)), $provider_id, $((provider_id * 10)), true),
    ($((provider_id * 10 + 1)), $provider_id, $((provider_id * 10 + 1)), true);
EOSQL
    done
    
    cat >> "$sql_file" <<'EOSQL'

COMMIT;

-- 验证
SELECT COUNT(*) AS provider_count FROM providers WHERE id BETWEEN 9010 AND 9099;
SELECT COUNT(*) AS credential_count FROM credentials WHERE id BETWEEN 9010 AND 9099;
SELECT COUNT(*) AS binding_count FROM credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9099;
EOSQL

    # 执行 SQL
    if [[ -n "$DB_PASSWORD" ]]; then
        PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$sql_file"
    else
        psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$sql_file"
    fi
    
    log_success "Credentials 注入完成"
}

# ── 启动 gateway ──
start_gateway() {
    log_info "启动 gateway (port $GATEWAY_PORT)..."
    
    # 检查 gateway 是否已运行
    if curl -sS --max-time 1 "http://localhost:$GATEWAY_PORT/healthz" > /dev/null 2>&1; then
        log_info "Gateway 已在运行"
        return 0
    fi
    
    cd "$ROOT_DIR"
    
    # 加载 .env（若存在），并补齐启动必需的环境变量。
    # gateway 需要 DATABASE_URL 才能启用 routing executor，需要 CORS 才不会 panic。
    if [[ -f .env ]]; then
        set -a
        # shellcheck disable=SC1091
        source <(grep -v '^#' .env | grep -v '^$' | sed 's/^/export /')
        set +a
    fi
    export DATABASE_URL="${DATABASE_URL:-postgres://${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable}"
    export LLM_GATEWAY_CORS_ORIGINS="${LLM_GATEWAY_CORS_ORIGINS:-*}"
    export LLM_GATEWAY_LISTEN="${LLM_GATEWAY_LISTEN:-:${GATEWAY_PORT}}"
    # credential 加密 key：必须与注入 ciphertext 时用的 key 一致。
    # 默认用项目测试 key（03-local-mock-credential.sql 同款）。
    export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=}"
    
    # 启动 gateway (后台运行)
    ./gateway > /tmp/gateway-loadtest.log 2>&1 &
    local pid=$!
    echo $pid > /tmp/gateway-loadtest.pid
    
    log_info "等待 gateway 启动 (DATABASE_URL=$DATABASE_URL)..."
    local retries=0
    while [[ $retries -lt 30 ]]; do
        if curl -sS --max-time 1 "http://localhost:$GATEWAY_PORT/healthz" > /dev/null 2>&1; then
            log_success "Gateway 已启动 (PID $pid)"
            return 0
        fi
        # 进程提前退出说明启动失败，打印日志帮助排查
        if ! kill -0 $pid 2>/dev/null; then
            log_error "Gateway 进程已退出，最后 30 行日志："
            tail -30 /tmp/gateway-loadtest.log || true
            exit 1
        fi
        sleep 1
        retries=$((retries + 1))
    done
    
    log_error "Gateway 启动超时"
    tail -20 /tmp/gateway-loadtest.log || true
    exit 1
}

# ── 运行基准压力测试 ──
run_baseline_stress_test() {
    log_info "运行基准压力测试 (所有 mock 健康状态)..."
    
    # 重置所有 mock 到健康状态
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/reset" > /dev/null 2>&1 || true
    done
    
    sleep 2
    
    cd "$ROOT_DIR"
    
    python3 scripts/loadtest-stress.py \
        --clients $NUM_CLIENTS \
        --rounds $NUM_ROUNDS_PER_CLIENT \
        --mode gateway \
        --gateway "http://localhost:$GATEWAY_PORT" \
        --model "gpt-4o" \
        --mocks "$(for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do echo -n "http://localhost:$((MOCK_START_PORT + i)),"; done | sed 's/,$//')" \
        2>&1 | tee /tmp/loadtest-baseline-$(date +%s).log
    
    log_success "基准压力测试完成"
}

# ── 运行故障注入测试 ──
run_fault_injection_test() {
    log_info "运行故障注入压力测试..."
    
    # 场景 1: 30% mock slow
    log_info "场景 1: 30% mock slow (5-10s latency)"
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS * 3 / 10))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/state" \
            -H 'Content-Type: application/json' \
            -d '{"mode":"slow","ttl_seconds":0}' > /dev/null 2>&1 || true
    done
    
    sleep 2
    
    python3 scripts/loadtest-stress.py \
        --clients $NUM_CLIENTS \
        --rounds $((NUM_ROUNDS_PER_CLIENT / 2)) \
        --mode gateway \
        --gateway "http://localhost:$GATEWAY_PORT" \
        --model "gpt-4o" \
        2>&1 | tee /tmp/loadtest-slow-$(date +%s).log
    
    # 场景 2: 20% mock rate_limited
    log_info "场景 2: 20% mock rate_limited"
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/reset" > /dev/null 2>&1 || true
    done
    
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS * 2 / 10))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/state" \
            -H 'Content-Type: application/json' \
            -d '{"mode":"rate_limited","ttl_seconds":0}' > /dev/null 2>&1 || true
    done
    
    sleep 2
    
    python3 scripts/loadtest-stress.py \
        --clients $NUM_CLIENTS \
        --rounds $((NUM_ROUNDS_PER_CLIENT / 2)) \
        --mode gateway \
        --gateway "http://localhost:$GATEWAY_PORT" \
        --model "gpt-4o" \
        2>&1 | tee /tmp/loadtest-ratelimit-$(date +%s).log
    
    # 场景 3: 混合故障 (flaky + server_error)
    log_info "场景 3: 混合故障 (30% flaky + 20% server_error)"
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/reset" > /dev/null 2>&1 || true
    done
    
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS * 3 / 10))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/state" \
            -H 'Content-Type: application/json' \
            -d '{"mode":"flaky","ttl_seconds":0}' > /dev/null 2>&1 || true
    done
    
    for i in $(seq $((NUM_MOCK_PROVIDERS * 3 / 10 + 1)) $((NUM_MOCK_PROVIDERS * 5 / 10))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/state" \
            -H 'Content-Type: application/json' \
            -d '{"mode":"server_error","ttl_seconds":0}' > /dev/null 2>&1 || true
    done
    
    sleep 2
    
    python3 scripts/loadtest-stress.py \
        --clients $NUM_CLIENTS \
        --rounds $((NUM_ROUNDS_PER_CLIENT / 2)) \
        --mode gateway \
        --gateway "http://localhost:$GATEWAY_PORT" \
        --model "gpt-4o" \
        2>&1 | tee /tmp/loadtest-mixed-$(date +%s).log
    
    # 重置所有 mock
    log_info "重置所有 mock 到健康状态"
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/reset" > /dev/null 2>&1 || true
    done
    
    log_success "故障注入测试完成"
}

# ── 运行动态故障注入测试 ──
run_dynamic_fault_injection_test() {
    log_info "运行动态故障注入测试 (测试中随机注入故障)..."
    
    # 重置所有 mock
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        curl -sS --max-time 2 -X POST "http://localhost:$port/admin/reset" > /dev/null 2>&1 || true
    done
    
    sleep 2
    
    python3 scripts/loadtest-stress.py \
        --clients $NUM_CLIENTS \
        --rounds $NUM_ROUNDS_PER_CLIENT \
        --mode gateway \
        --gateway "http://localhost:$GATEWAY_PORT" \
        --model "gpt-4o" \
        --fault-injection \
        2>&1 | tee /tmp/loadtest-dynamic-$(date +%s).log
    
    log_success "动态故障注入测试完成"
}

# ── 生成测试报告 ──
generate_report() {
    log_info "生成测试报告..."
    
    local report_file="/tmp/loadtest-report-$(date +%Y%m%d-%H%M%S).md"
    
    cat > "$report_file" <<EOF
# LLM Gateway 压力测试报告

**测试时间**: $(date '+%Y-%m-%d %H:%M:%S')

## 测试配置

- **Mock 供应商数量**: $NUM_MOCK_PROVIDERS
- **并发客户端数**: $NUM_CLIENTS
- **每客户端请求轮次**: $NUM_ROUNDS_PER_CLIENT
- **总请求数**: $((NUM_CLIENTS * NUM_ROUNDS_PER_CLIENT))
- **Gateway 端口**: $GATEWAY_PORT

## Mock 供应商状态

EOF

    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        local token=$(printf "mock-%02d" $i)
        echo "### $token (port $port)" >> "$report_file"
        echo '```json' >> "$report_file"
        curl -sS --max-time 2 "http://localhost:$port/admin/metrics" 2>/dev/null | jq '.' >> "$report_file" || echo "{}" >> "$report_file"
        echo '```' >> "$report_file"
        echo "" >> "$report_file"
    done
    
    cat >> "$report_file" <<EOF

## 测试场景

1. **基准测试**: 所有 mock 健康状态
2. **故障场景 1**: 30% mock slow (5-10s latency)
3. **故障场景 2**: 20% mock rate_limited
4. **故障场景 3**: 混合故障 (30% flaky + 20% server_error)
5. **动态故障注入**: 测试过程中随机注入故障

## 测试日志

所有测试日志保存在 \`/tmp/loadtest-*.log\`

## 总结

- 验证了 gateway 在高并发下的路由和故障转移能力
- 验证了 sticky routing 在多轮会话中的一致性
- 验证了在各种故障场景下的容错能力

EOF

    log_success "测试报告已生成: $report_file"
    cat "$report_file"
}

# ── 清理资源 ──
cleanup() {
    log_info "清理测试资源..."
    
    # 停止 gateway
    if [[ -f "/tmp/gateway-loadtest.pid" ]]; then
        local pid=$(cat /tmp/gateway-loadtest.pid)
        if kill -0 $pid 2>/dev/null; then
            kill $pid
            log_info "已停止 gateway (PID $pid)"
        fi
        rm -f /tmp/gateway-loadtest.pid
    fi
    
    # 停止所有 mock
    for i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
        local port=$((MOCK_START_PORT + i))
        if [[ -f "/tmp/mock-$port.pid" ]]; then
            local pid=$(cat /tmp/mock-$port.pid)
            if kill -0 $pid 2>/dev/null; then
                kill $pid
            fi
            rm -f /tmp/mock-$port.pid
        fi
    done
    
    pkill -f "server-v2.py" 2>/dev/null || true
    
    log_success "清理完成"
}

# ── 主流程 ──
main() {
    local command="${1:-all}"
    
    case "$command" in
        check)
            check_dependencies
            ;;
        build)
            build_gateway
            ;;
        start-mocks)
            start_mock_providers
            ;;
        inject-creds)
            inject_credentials
            ;;
        start-gateway)
            start_gateway
            ;;
        test-baseline)
            run_baseline_stress_test
            ;;
        test-faults)
            run_fault_injection_test
            ;;
        test-dynamic)
            run_dynamic_fault_injection_test
            ;;
        report)
            generate_report
            ;;
        cleanup)
            cleanup
            ;;
        all)
            trap cleanup EXIT
            
            log_info "开始完整的压力测试流程..."
            echo ""
            
            check_dependencies
            build_gateway
            start_mock_providers
            inject_credentials
            start_gateway
            
            log_info "等待系统稳定..."
            sleep 5
            
            run_baseline_stress_test
            run_fault_injection_test
            run_dynamic_fault_injection_test
            
            generate_report
            
            log_success "全部测试完成！"
            ;;
        *)
            cat <<EOF
用法: $0 <command>

命令:
  check           - 检查依赖
  build           - 编译 gateway
  start-mocks     - 启动 mock 供应商
  inject-creds    - 注入 credentials 到数据库
  start-gateway   - 启动 gateway
  test-baseline   - 运行基准压力测试
  test-faults     - 运行故障注入测试
  test-dynamic    - 运行动态故障注入测试
  report          - 生成测试报告
  cleanup         - 清理测试资源
  all             - 运行完整测试流程 (默认)

示例:
  $0 all          # 运行完整测试
  $0 start-mocks  # 只启动 mock
  $0 cleanup      # 清理资源
EOF
            exit 1
            ;;
    esac
}

main "$@"
