#!/bin/bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# W1 缺口① per-model 熔断器验证脚本
# 用途：本地验证单模型故障不连坐行为
# 
# 前置条件：
# 1. 本地 PostgreSQL 运行中（llm_gateway 数据库）
# 2. Redis 运行中
# 3. 已构建最新 binary

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# 颜色输出
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

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

# 检查前置条件
check_prerequisites() {
    log_info "检查前置条件..."
    
    # 检查 PostgreSQL
    if ! psql -h localhost -U postgres -d llm_gateway -c "SELECT 1" &>/dev/null; then
        log_error "PostgreSQL 未运行或 llm_gateway 数据库不存在"
        exit 1
    fi
    log_success "PostgreSQL 运行正常"
    
    # 检查 Redis
    if ! redis-cli ping &>/dev/null; then
        log_error "Redis 未运行"
        exit 1
    fi
    log_success "Redis 运行正常"
    
    # 检查 binary
    if [[ ! -f "./cmd/gateway/gateway" ]]; then
        log_info "binary 不存在，开始构建..."
        go build -o cmd/gateway/gateway ./cmd/gateway
    fi
    log_success "binary 准备就绪"
}

# 准备测试数据
setup_test_data() {
    log_info "准备测试数据..."
    
    # 创建测试凭据和模型配置
    psql -h localhost -U postgres -d llm_gateway <<EOF
-- 确保有测试 provider 和 credential
INSERT INTO providers (id, name, base_url, enabled) 
VALUES (999, 'test-provider', 'http://localhost:9999', true)
ON CONFLICT (id) DO UPDATE SET enabled = true;

INSERT INTO credentials (id, provider_id, api_key, enabled, billing_mode)
VALUES (9999, 999, 'test-key', true, 'paid')
ON CONFLICT (id) DO UPDATE SET enabled = true;

-- 清理旧的熔断状态
UPDATE credentials SET circuit_state = 'closed', circuit_opened_at = NULL, circuit_cooling_until = NULL
WHERE id = 9999;
EOF
    
    log_success "测试数据准备完成"
}

# 启动测试 Gateway（后台）
start_test_gateway() {
    log_info "启动测试 Gateway（端口 18090）..."
    
    # 停止旧进程
    pkill -f "gateway.*18090" 2>/dev/null || true
    sleep 1
    
    # 启动新进程
    PORT=18090 \
    DB_HOST=localhost \
    DB_PORT=5432 \
    DB_USER=postgres \
    DB_PASSWORD='' \
    DB_NAME=llm_gateway \
    REDIS_ADDR=localhost:6379 \
    ./cmd/gateway/gateway > /tmp/llm-gateway-test.log 2>&1 &
    
    GATEWAY_PID=$!
    echo $GATEWAY_PID > /tmp/llm-gateway-test.pid
    
    # 等待启动
    log_info "等待 Gateway 启动..."
    for i in {1..30}; do
        if curl -sf http://localhost:18090/health &>/dev/null; then
            log_success "Gateway 启动成功 (PID: $GATEWAY_PID)"
            return 0
        fi
        sleep 1
    done
    
    log_error "Gateway 启动失败"
    cat /tmp/llm-gateway-test.log
    exit 1
}

# 验证场景 1：gpt-4 熔断，gpt-3.5-turbo 正常
test_scenario_1() {
    log_info "=== 场景 1：gpt-4 熔断，gpt-3.5-turbo 正常 ==="
    
    # 模拟 gpt-4 连续失败（触发熔断）
    log_info "1. 模拟 gpt-4 连续失败..."
    for i in {1..3}; do
        curl -sf -X POST http://localhost:18090/v1/chat/completions \
            -H "Authorization: Bearer test-key" \
            -H "Content-Type: application/json" \
            -d '{
                "model": "gpt-4",
                "messages": [{"role": "user", "content": "test"}],
                "stream": false
            }' || true
        sleep 0.5
    done
    
    # 查询熔断状态
    log_info "2. 查询 gpt-4 熔断状态..."
    STATE=$(psql -h localhost -U postgres -d llm_gateway -t -c \
        "SELECT circuit_state FROM credentials WHERE id = 9999" | xargs)
    
    if [[ "$STATE" == "open" ]]; then
        log_success "✅ gpt-4 已熔断（circuit_state = open）"
    else
        log_warning "⚠️  gpt-4 未熔断（circuit_state = ${STATE}）"
    fi
    
    # 测试 gpt-3.5-turbo（应该正常）
    log_info "3. 测试 gpt-3.5-turbo（应该不受影响）..."
    RESPONSE=$(curl -sf -X POST http://localhost:18090/v1/chat/completions \
        -H "Authorization: Bearer test-key" \
        -H "Content-Type: application/json" \
        -d '{
            "model": "gpt-3.5-turbo",
            "messages": [{"role": "user", "content": "test"}],
            "stream": false
        }' 2>&1 || echo "FAILED")
    
    if [[ "$RESPONSE" != "FAILED" ]]; then
        log_success "✅ gpt-3.5-turbo 请求通过（未被连坐）"
    else
        log_error "❌ gpt-3.5-turbo 请求失败（被连坐了！）"
    fi
    
    echo ""
}

# 验证场景 2：查看日志中的 per-model 键
test_scenario_2() {
    log_info "=== 场景 2：验证日志中的 per-model 键 ==="
    
    log_info "查询最近的熔断日志..."
    grep "circuit" /tmp/llm-gateway-test.log | tail -10 | while read -r line; do
        if echo "$line" | grep -q "key="; then
            KEY=$(echo "$line" | grep -o 'key=[^ ]*' | cut -d= -f2)
            if [[ "$KEY" =~ ^[0-9]+/gpt- ]]; then
                log_success "✅ 发现 per-model 键: $KEY"
            else
                log_warning "⚠️  旧键格式: $KEY"
            fi
        fi
    done
    
    echo ""
}

# 验证场景 3：上卷态验证
test_scenario_3() {
    log_info "=== 场景 3：DB 上卷态验证 ==="
    
    log_info "查询 credentials.circuit_state..."
    psql -h localhost -U postgres -d llm_gateway -c \
        "SELECT id, api_key, circuit_state, circuit_opened_at, circuit_cooling_until 
         FROM credentials WHERE id = 9999;"
    
    log_info "说明："
    echo "  - circuit_state = open/quarantined: 说明有模型处于熔断状态"
    echo "  - circuit_state = closed: 说明所有模型都正常"
    echo "  - 上卷态应反映该凭据所有模型的最差状态"
    
    echo ""
}

# 清理
cleanup() {
    log_info "清理测试环境..."
    
    # 停止 Gateway
    if [[ -f /tmp/llm-gateway-test.pid ]]; then
        PID=$(cat /tmp/llm-gateway-test.pid)
        kill $PID 2>/dev/null || true
        rm /tmp/llm-gateway-test.pid
    fi
    
    # 清理测试数据
    psql -h localhost -U postgres -d llm_gateway <<EOF
DELETE FROM credentials WHERE id = 9999;
DELETE FROM providers WHERE id = 999;
EOF
    
    log_success "清理完成"
}

# 主流程
main() {
    log_info "W1 缺口① per-model 熔断器验证"
    log_info "================================"
    echo ""
    
    # 捕获退出信号
    trap cleanup EXIT
    
    check_prerequisites
    echo ""
    
    setup_test_data
    echo ""
    
    start_test_gateway
    echo ""
    
    sleep 2
    
    test_scenario_1
    test_scenario_2
    test_scenario_3
    
    log_info "================================"
    log_success "验证完成！"
    echo ""
    log_info "查看完整日志: tail -f /tmp/llm-gateway-test.log"
}

main "$@"
