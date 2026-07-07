#!/usr/bin/env bash
# ====================================================================
# 快速启动脚本 - 用于日常开发测试
# ====================================================================
# 快速启动 4 个 mock + gateway，运行简单压力测试
# ====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# 配置
NUM_MOCKS=4
GATEWAY_PORT=8080

echo "🚀 快速启动本地测试环境"
echo ""

# 1. 启动 4 个 mock
echo "📦 启动 $NUM_MOCKS 个 mock 供应商..."
cd "$SCRIPT_DIR/mocks/llm-mock-upstream"

for i in $(seq 0 $((NUM_MOCKS - 1))); do
    port=$((19080 + i))
    token=$(printf "mock-%02d" $i)
    
    if curl -sS --max-time 1 "http://localhost:$port/healthz" > /dev/null 2>&1; then
        echo "  ✓ $token (port $port) 已运行"
        continue
    fi
    
    MOCK_PORT=$port \
    MOCK_TOKEN=$token \
    MOCK_STATE_FILE="/tmp/mock-state-$port.json" \
    python3 server-v2.py > "/tmp/mock-$port.log" 2>&1 &
    
    echo "  ✓ 启动 $token (port $port, PID $!)"
done

sleep 2

# 2. 验证 mock
echo ""
echo "🔍 验证 mock 可用性..."
for i in $(seq 0 $((NUM_MOCKS - 1))); do
    port=$((19080 + i))
    if curl -sS --max-time 2 "http://localhost:$port/healthz" | jq -c '{token, mode}'; then
        echo "  ✓ mock-$(printf "%02d" $i) 可用"
    else
        echo "  ✗ mock-$(printf "%02d" $i) 不可用"
    fi
done

# 3. 注入 credentials (如果需要)
echo ""
echo "💉 注入 credentials (使用已有的 SQL)..."
echo "请手动执行:"
echo "  psql -f $ROOT_DIR/sql/scripts/04-loadtest-mock-credentials.sql"
echo ""
read -p "按 Enter 继续 (确认已执行 SQL)..."

# 4. 启动 gateway
echo ""
echo "🌐 启动 gateway..."
cd "$ROOT_DIR"

if curl -sS --max-time 1 "http://localhost:$GATEWAY_PORT/healthz" > /dev/null 2>&1; then
    echo "  ✓ Gateway 已运行"
else
    if [[ ! -f "./gateway" ]]; then
        echo "  编译 gateway..."
        go build -o gateway cmd/gateway/main.go
    fi
    
    ./gateway > /tmp/gateway-loadtest.log 2>&1 &
    echo "  ✓ Gateway 启动 (PID $!, 日志: /tmp/gateway-loadtest.log)"
    
    echo "  等待 gateway 启动..."
    for i in $(seq 1 30); do
        if curl -sS --max-time 1 "http://localhost:$GATEWAY_PORT/healthz" > /dev/null 2>&1; then
            echo "  ✓ Gateway 就绪"
            break
        fi
        sleep 1
    done
fi

# 5. 运行简单压力测试
echo ""
echo "🧪 运行快速压力测试 (10 客户端 × 20 轮)..."
python3 scripts/loadtest-stress.py \
    --clients 10 \
    --rounds 20 \
    --mode gateway \
    --gateway "http://localhost:$GATEWAY_PORT" \
    --model "gpt-4o"

echo ""
echo "✅ 完成！"
echo ""
echo "接下来可以:"
echo "  - 查看 mock 状态: curl http://localhost:19080/admin/metrics | jq"
echo "  - 修改 mock 状态: bash scripts/mock-state-orchestrator.sh set-all slow"
echo "  - 重新测试: python3 scripts/loadtest-stress.py --clients 10 --rounds 20 --mode gateway --gateway http://localhost:8080 --model gpt-4o"
echo "  - 清理: pkill -f 'server-v2.py'; pkill -f gateway"
