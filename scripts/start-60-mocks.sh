#!/usr/bin/env bash
# ====================================================================
# 启动 60 个 Mock 供应商实例
# ====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MOCK_DIR="$SCRIPT_DIR/mocks/llm-mock-upstream"

NUM_MOCKS=60
START_PORT=19080

GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${BLUE}[start-mocks]${NC} $*"; }
ok() { echo -e "${GREEN}[start-mocks]✓${NC} $*"; }
warn() { echo -e "${YELLOW}[start-mocks]⚠${NC} $*"; }

cd "$MOCK_DIR"

log "启动 $NUM_MOCKS 个 mock 供应商实例..."

started=0
skipped=0

for i in $(seq 0 $((NUM_MOCKS - 1))); do
    port=$((START_PORT + i))
    token=$(printf "mock-%02d" $i)
    
    # 检查端口是否已被占用
    if curl -sS --max-time 1 "http://localhost:$port/healthz" > /dev/null 2>&1; then
        log "  $token (port $port) 已在运行"
        skipped=$((skipped + 1))
        continue
    fi
    
    # 启动 mock 进程
    MOCK_PORT=$port \
    MOCK_TOKEN=$token \
    MOCK_STATE_FILE="/tmp/mock-state-$port.json" \
    python3 server-v2.py > "/tmp/mock-$port.log" 2>&1 &
    
    pid=$!
    echo $pid > "/tmp/mock-$port.pid"
    started=$((started + 1))
done

# 等待所有 mock 启动
log "等待 mock 启动..."
sleep 5

# 验证可用性
log "验证 mock 可用性..."
healthy=0
unhealthy=0

for i in $(seq 0 $((NUM_MOCKS - 1))); do
    port=$((START_PORT + i))
    if curl -sS --max-time 2 "http://localhost:$port/healthz" > /dev/null 2>&1; then
        healthy=$((healthy + 1))
    else
        warn "  mock-$(printf "%02d" $i) (port $port) 未响应"
        unhealthy=$((unhealthy + 1))
    fi
done

ok "启动完成: $started 个新启动, $skipped 个已运行"
ok "健康检查: $healthy/$NUM_MOCKS 个可用"

if [[ $unhealthy -gt 0 ]]; then
    warn "$unhealthy 个 mock 未响应，请检查日志: /tmp/mock-*.log"
fi

if [[ $healthy -lt $((NUM_MOCKS / 2)) ]]; then
    echo "❌ 错误: 超过一半的 mock 不可用，测试无法进行"
    exit 1
fi

ok "Mock 供应商就绪，可以启动 gateway"
