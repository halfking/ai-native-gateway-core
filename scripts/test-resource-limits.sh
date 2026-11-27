#!/usr/bin/env bash
# test-resource-limits.sh —— 资源限制端到端验收（resource-limiter 集成 §4.3 / §8.1）。
#
# 在 Linux 测试机上运行；macOS 上自动降级为「仅单元/集成测试 + 提示」。
# 覆盖验收点：
#   1. 单元/集成测试全绿（limiter / controller resource_limits 审计）
#   2. Linux: cgroup 目录创建 + memory.max/cpu.max/pids.max 三文件写入正确
#   3. Linux: 插件进程归组（cgroup.procs）
#   4. Linux: OOM 行为（memory.events oom_kill 计数，8MB 限制吃 64MB）
#   5. Linux: 插件停止后 cgroup 清理
#
# 用法：
#   需 root 或 cgroup 委派（delegated cgroup）环境：
#     sudo bash scripts/test-resource-limits.sh
#   或容器（--privileged -v /sys/fs/cgroup:/sys/fs/cgroup）。
#
# 输出：PASS/FAIL 汇总 + 各验收点明细，退出码 0=全绿。
set -u

cd "$(dirname "$0")/.."
GO=${GO:-go}
PASS=0
FAIL=0

step() { printf '\n=== %s ===\n' "$1"; }
ok()   { printf '  ✓ %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  ✗ %s\n' "$1"; FAIL=$((FAIL+1)); }

IS_LINUX=$(uname -s)

step "0. 前置检查"
if ! command -v "$GO" >/dev/null 2>&1; then
    bad "go not found"
    exit 1
fi
ok "go = $($GO version)"

step "1. 单元 + 集成测试（全平台）"
if "$GO" test ./plugin-runtime/limiter/ ./plugin-runtime/ -count=1 >/tmp/rl-unit.log 2>&1; then
    ok "limiter + supervisor suites ($(grep -c '^\s*--- PASS' /tmp/rl-unit.log 2>/dev/null || echo '?') pass)"
else
    bad "unit/integration suites failed — see /tmp/rl-unit.log"
    tail -20 /tmp/rl-unit.log
fi
if "$GO" test ./plugin-runtime/controller/ -run 'TestControllerStartsWithDefaultResourceLimits|TestResourceLimits' -count=1 >/tmp/rl-controller.log 2>&1; then
    ok "controller resource_limits audit suite"
else
    bad "controller resource_limits audit failed — see /tmp/rl-controller.log"
    tail -20 /tmp/rl-controller.log
fi

if [ "$IS_LINUX" != "Linux" ]; then
    step "2-5. cgroup 硬限制端到端"
    printf '  ⚠ non-Linux host (%s): cgroup hard-limit e2e requires a Linux test machine.\n' "$IS_LINUX"
    printf '    Run on a Linux box (root or cgroup delegation):\n      sudo %s test ./plugin-runtime/controller/ -run TestE2ECgroup -v\n' "$GO"
    printf '    Or: sudo bash %s\n' "$0"
    printf '\nResult: %d passed, %d failed (e2e deferred to Linux host)\n' "$PASS" "$FAIL"
    exit 0
fi

step "2-5. cgroup 硬限制端到端（Linux）"
if [ "$(id -u)" != "0" ] && [ ! -w /sys/fs/cgroup ]; then
    bad "need root or cgroup delegation to write /sys/fs/cgroup — rerun with sudo"
    printf '\nResult: %d passed, %d failed\n' "$PASS" "$FAIL"
    exit 1
fi
if "$GO" test ./plugin-runtime/controller/ -run 'TestE2ECgroup' -count=1 -v >/tmp/rl-e2e.log 2>&1; then
    ok "cgroup hard-limits e2e (3-file values, proc assignment, cleanup)"
else
    bad "cgroup hard-limits e2e failed — see /tmp/rl-e2e.log"
    grep -E '^(--- |=== RUN|    )' /tmp/rl-e2e.log | tail -30
fi
if grep -q 'oom_kill count = ' /tmp/rl-e2e.log; then
    ok "OOM behavior: $(grep -o 'oom_kill count = [0-9]*' /tmp/rl-e2e.log | head -1)"
else
    bad "OOM behavior not confirmed (no oom_kill log line) — see /tmp/rl-e2e.log"
fi

step "6. 指标导出面冒烟（§5.3）"
if "$GO" test ./metrics/ -run 'TestPluginResource|TestRecordPlugin' -count=1 >/tmp/rl-metrics.log 2>&1; then
    ok "llmgw_plugin_* metrics registered + record helpers"
else
    bad "metrics smoke failed — see /tmp/rl-metrics.log"
    tail -10 /tmp/rl-metrics.log
fi

printf '\nResult: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
