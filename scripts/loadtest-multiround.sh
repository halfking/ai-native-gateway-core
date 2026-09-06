#!/usr/bin/env bash
# Multi-Round Test Runner - 跨轮保持状态, 模拟长期运行
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECKPOINT_DIR="/tmp/loadtest-checkpoints"
mkdir -p "$CHECKPOINT_DIR"

# Mock 端口范围可由环境变量覆盖（默认 12 个：19080-19091）。
MOCK_START_PORT="${MOCK_START_PORT:-19080}"
NUM_MOCK_PROVIDERS="${NUM_MOCK_PROVIDERS:-12}"
MOCK_PORTS=()
for _i in $(seq 0 $((NUM_MOCK_PROVIDERS - 1))); do
  MOCK_PORTS+=($((MOCK_START_PORT + _i)))
done

usage() {
  echo "用法: $0 <rounds> <scenario_list>"
  echo "示例: $0 10 S4,S5,S17  # 跑 10 轮, 每轮依次 S4/S5/S17"
  echo ""
  echo "特性:"
  echo "  - 每轮开始前随机注入故障 (模拟生产随机性)"
  echo "  - 每 3 轮 checkpoint 一次 (保存 mock 状态)"
  echo "  - 跨轮累计 metrics"
  exit 1
}

[[ $# -lt 2 ]] && usage

MAX_ROUNDS=$1
SCENARIOS=$2

inject_random_failure() {
  local round=$1
  # 随机选一个 mock + 随机选一个故障模式（用全局 MOCK_PORTS，支持任意数量）
  local n=${#MOCK_PORTS[@]}
  MODES=(slow rate_limited server_error flaky)
  
  RANDOM_MOCK=${MOCK_PORTS[$((RANDOM % n))]}
  RANDOM_MODE=${MODES[$((RANDOM % 4))]}
  
  echo "  [随机故障注入] mock-$RANDOM_MOCK → $RANDOM_MODE (Round $round)"
  bash "$SCRIPT_DIR/mock-state-orchestrator.sh" set "http://localhost:$RANDOM_MOCK" "$RANDOM_MODE" 30  # TTL=30s
}

save_checkpoint() {
  local round=$1
  echo "  [Checkpoint Round $round]"
  
  # 保存所有 mock 的 metrics 到文件
  for port in "${MOCK_PORTS[@]}"; do
    curl -sS "http://localhost:$port/admin/metrics" > "$CHECKPOINT_DIR/mock-$port-round-$round.json" 2>/dev/null || true
  done
  
  # 保存 mock 状态
  for port in "${MOCK_PORTS[@]}"; do
    curl -sS "http://localhost:$port/admin/state" > "$CHECKPOINT_DIR/mock-$port-state-round-$round.json" 2>/dev/null || true
  done
  
  echo "  Checkpoint saved to $CHECKPOINT_DIR/"
}

restore_checkpoint() {
  local round=$1
  echo "  [恢复 Checkpoint Round $round]"
  
  # 从文件恢复 mock 状态 (简化版: 只恢复模式)
  for port in "${MOCK_PORTS[@]}"; do
    if [[ -f "$CHECKPOINT_DIR/mock-$port-state-round-$round.json" ]]; then
      MODE=$(jq -r '.mode' "$CHECKPOINT_DIR/mock-$port-state-round-$round.json" 2>/dev/null || echo "healthy")
      bash "$SCRIPT_DIR/mock-state-orchestrator.sh" set "http://localhost:$port" "$MODE" 0 || true
    fi
  done
}

run_scenario() {
  local scenario=$1
  local script="$SCRIPT_DIR/loadtest-scenarios/$scenario.sh"
  
  if [[ ! -f "$script" ]]; then
    echo "  ⚠️  场景 $scenario 不存在, 跳过"
    return
  fi
  
  echo "  [运行] $scenario"
  bash "$script" > /tmp/loadtest-$scenario-$(date +%s).log 2>&1 || echo "  ⚠️  $scenario 失败"
}

echo "═══════════════════════════════════════════"
echo "  Multi-Round Loadtest"
echo "  Rounds: $MAX_ROUNDS"
echo "  Scenarios: $SCENARIOS"
echo "═══════════════════════════════════════════"
echo ""

IFS=',' read -ra SCENARIO_LIST <<< "$SCENARIOS"

ROUND=0
while [[ $ROUND -lt $MAX_ROUNDS ]]; do
  echo ""
  echo "═══ Round $((ROUND+1))/$MAX_ROUNDS ═══"
  
  # 每轮开始前随机注入故障
  if [[ $((RANDOM % 3)) == 0 ]]; then  # 33% 概率注入
    inject_random_failure $ROUND
  fi
  
  # 跑所有场景
  for scenario in "${SCENARIO_LIST[@]}"; do
    run_scenario "$scenario"
  done
  
  # 每 3 轮 checkpoint
  if [[ $((ROUND % 3)) == 0 ]] && [[ $ROUND -gt 0 ]]; then
    save_checkpoint $ROUND
  fi
  
  ROUND=$((ROUND+1))
  sleep 5  # 轮间间隔
done

echo ""
echo "═══════════════════════════════════════════"
echo "  ✓ $MAX_ROUNDS 轮完成"
echo "  Checkpoints: $CHECKPOINT_DIR/"
echo "═══════════════════════════════════════════"

# 最终汇总
echo ""
echo "=== 最终 Metrics 汇总 ==="
for port in "${MOCK_PORTS[@]}"; do
  curl -sS "http://localhost:$port/admin/metrics" | jq -c '{token, counters}'
done
