#!/usr/bin/env bash
# =============================================================================
# 多维度画像测试 v2 — 10 场景完整验证（修复 4 项问题后）
#
# 场景清单:
#   原有 6 场景（重跑）:
#     S1 成本优化 (plan vs PAYG)        — F 组禁用，验证 Round1 优先
#     S2 并发能力差异化                  — 中等负载均匀分布
#     S3 配额耗尽与恢复                  — C 组低配额，验证 failover
#     S4 延迟/质量降权                   — J 组 flaky(30%)，验证 0% 流量
#     S5 混合故障韧性 (修复后)           — B 组改 flaky(30%) 而非 server_error(50%)
#     S6 高峰动态调度                    — 高负载启用更多供应商
#   新增 4 场景:
#     S7 Sticky 连续性 ⭐(修复问题2)     — --sessions 10 --session-reuse-ratio 0.8
#     S8 流式测试 (SSE)                  — --stream，验证 TTFB 与 [DONE]
#     S9 长 prompt                       — --prompt-size long，验证 context 处理
#     S10 周期性配额恢复 ⭐              — 短窗口(60s) 验证 quota_recover_at
#
# 前置: PostgreSQL + 60 mocks + gateway(:8082) 均已启动
# =============================================================================
set -uo pipefail
cd "$(dirname "$0")/.."   # 项目根

GATEWAY="http://localhost:8082"
KEYS="sk-stress-test-01-hash-0000000000000000000000000000000000000001,sk-stress-test-02-hash-0000000000000000000000000000000000000002,sk-stress-test-03-hash-0000000000000000000000000000000000000003"
MODELS="loadtest-mini-alpha,loadtest-mini-beta,loadtest-standard-alpha,loadtest-standard-beta,loadtest-pro-alpha"
RESULTS_DIR="/tmp/multidim-results"
mkdir -p "$RESULTS_DIR"

# ---------- helpers ----------
orch() { python3 scripts/mock-orchestrator-v2.py "$@"; }

# 直接通过 /admin/state 设置 mode（reset-group 不会清除 state.mode，这是已知 quirk）
set_group_state() {
  local group="$1" mode="$2"
  python3 - "$group" "$mode" <<'PY'
import json, urllib.request, sys
group, mode = sys.argv[1], sys.argv[2]
cfg = json.load(open("scripts/profiles/provider-profiles.json"))
for port in cfg["group_port_map"][group]:
    req = urllib.request.Request(f"http://localhost:{port}/admin/state",
        data=json.dumps({"mode": mode}).encode(),
        headers={"Content-Type": "application/json"}, method="POST")
    try:
        urllib.request.urlopen(req, timeout=3).read()
        print(f"  {group} port {port}: -> {mode}")
    except Exception as e:
        print(f"  {group} port {port}: ERR {e}")
PY
}

# 重置一个组的 mode 为 healthy（profile 字段由 reset-group 处理）
reset_mode() { set_group_state "$1" "healthy"; }

run_loadtest() {
  local name="$1"; shift
  local out="$RESULTS_DIR/${name}.json"
  echo -e "\n▶▶▶ $name"
  python3 scripts/loadtest-v2.py --gateway "$GATEWAY" --api-keys "$KEYS" \
      --output "$out" "$@" 2>&1 | tail -25
  echo "   [saved] $out"
}

prereq_check() {
  echo "=== prereq check ==="
  curl -sS "$GATEWAY/healthz" >/dev/null 2>&1 || { echo "✗ gateway down at $GATEWAY"; exit 1; }
  psql -d llm_gateway -tc "SELECT 1" >/dev/null 2>&1 || { echo "✗ DB down"; exit 1; }
  local n; n=$(pgrep -f 'server-v3.py' | wc -l | tr -d ' ')
  [ "${n:-0}" -ge 50 ] || { echo "✗ only ${n:-0}/60 mocks running"; exit 1; }
  echo "✓ gateway + DB + $n mocks OK"
}

reset_all() {
  echo "=== reset all groups to healthy defaults ==="
  for g in A B C D E F G H I J K L; do
    orch reset-group "$g" >/dev/null 2>&1
    reset_mode "$g" >/dev/null 2>&1
  done
  # 解除所有 provider manual_disabled
  psql -d llm_gateway -c "UPDATE providers SET manual_disabled=false WHERE id BETWEEN 9010 AND 9069;" >/dev/null 2>&1
  # 重置配额窗口
  orch reset-quota-all >/dev/null 2>&1
  echo "✓ all groups healthy, providers enabled, quotas reset"
}

# F 组（免费池 cost=0）在 P2C 成本过滤中 dominate（closePool: cost==0 常胜），
# 会使所有付费组拿不到流量。除专门验证免费池的场景外，全程禁用 F 组。
disable_free_pool() {
  orch disable-group F >/dev/null 2>&1
  echo "✓ F (free pool) disabled for scenario fidelity"
}

# ---------- main ----------
prereq_check
reset_all
disable_free_pool

# ============================================================================
# S1 成本优化验证（plan vs PAYG）— F 组已全局禁用
# ============================================================================
echo -e "\n========== S1: 成本优化 (plan vs PAYG) =========="
# C 组恢复配额（reset-quota-all 已做）
run_loadtest S1-cost-optimization --clients 80 --rounds 8 --models "$MODELS" --prompt-size short

# ============================================================================
# S2 并发能力差异化
# ============================================================================
echo -e "\n========== S2: 并发能力差异化 =========="
run_loadtest S2-concurrency-diff --clients 100 --rounds 6 --models "$MODELS" --prompt-size short

# ============================================================================
# S3 配额耗尽与恢复 — C 组低配额
# ============================================================================
echo -e "\n========== S3: 配额耗尽与恢复 =========="
# C 组每实例配额降到 2000 tokens（快速耗尽）
for port in 19090 19091 19092 19093 19094; do
  orch set-quota "$port" 2000 1800 >/dev/null 2>&1
done
run_loadtest S3-quota-failover --clients 60 --rounds 10 --models "$MODELS" --prompt-size short
reset_all; disable_free_pool

# ============================================================================
# S4 延迟/质量降权 — J 组 flaky(30%)
# ============================================================================
echo -e "\n========== S4: 延迟/质量降权 (J=flaky 30%) =========="
set_group_state J flaky
run_loadtest S4-quality-penalty --clients 80 --rounds 8 --models "$MODELS" --prompt-size short
reset_mode J

# ============================================================================
# S5 混合故障韧性（修复后）— B 组 flaky(30%) 而非 server_error(50%)  ★修复问题1
# 故障注入: G=slow, J=flaky(30%), B=flaky(30%), K=rate_limited = 20/60 (33%)
# ============================================================================
echo -e "\n========== S5: 混合故障韧性 (修复: B=flaky30% 而非 server_error50%) =========="
set_group_state G slow      # G 慢
set_group_state J flaky     # J 30% 错误
set_group_state B flaky     # ★ 修复: B 从 server_error(50%) 改为 flaky(30%)
set_group_state K rate_limited
run_loadtest S5-mixed-fault-fixed --clients 100 --rounds 6 --models "$MODELS" --prompt-size short
reset_all; disable_free_pool

# ============================================================================
# S6 高峰动态调度
# ============================================================================
echo -e "\n========== S6: 高峰动态调度 =========="
run_loadtest S6-peak-scheduling --clients 100 --rounds 6 --models "$MODELS" --prompt-size mixed

# ============================================================================
# S7 Sticky 连续性  ★修复问题2 — session 池 + 复用率 0.8
# ============================================================================
echo -e "\n========== S7: Sticky 连续性 (sessions=10, reuse=0.8) =========="
run_loadtest S7-sticky-session --clients 30 --rounds 20 --models "loadtest-mini-alpha" \
    --sessions 10 --session-reuse-ratio 0.8 --prompt-size short

# ============================================================================
# S8 流式测试 (SSE)
# ============================================================================
echo -e "\n========== S8: 流式 SSE =========="
run_loadtest S8-streaming --clients 40 --rounds 5 --models "$MODELS" --stream --prompt-size short

# ============================================================================
# S9 长 prompt
# ============================================================================
echo -e "\n========== S9: 长 prompt =========="
run_loadtest S9-long-prompt --clients 40 --rounds 5 --models "$MODELS" --prompt-size long

# ============================================================================
# S10 周期性配额恢复  ★新增 — 短窗口(60s) 验证 quota_recover_at
# ============================================================================
echo -e "\n========== S10: 周期性配额恢复 (60s 窗口) =========="
# C 组短窗口：总额 3000，窗口 60s
for port in 19090 19091 19092 19093 19094; do
  orch set-quota "$port" 3000 60 >/dev/null 2>&1
done
# 第一波：耗尽 C 组
echo "  --- wave 1: exhaust C group ---"
run_loadtest S10-quota-recovery-w1 --clients 40 --rounds 4 --models "$MODELS" --prompt-size short
echo "  --- waiting 65s for quota window to reset ---"
sleep 65
# 第二波：验证 C 组恢复
echo "  --- wave 2: verify C group recovered ---"
run_loadtest S10-quota-recovery-w2 --clients 40 --rounds 4 --models "$MODELS" --prompt-size short
reset_all

echo -e "\n========== ALL 10 SCENARIOS COMPLETE =========="
echo "Results: $RESULTS_DIR/"
ls -la "$RESULTS_DIR/"
