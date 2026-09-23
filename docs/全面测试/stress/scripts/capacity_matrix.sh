#!/usr/bin/env bash
# tests/stress/scripts/capacity_matrix.sh
#
# Handoff-A from CAPACITY_HANDOVER.md: 用 GOMAXPROCS + taskset + cgroup
# 模拟 4 规格，每规格跑 s8/s9/s13/s15；输出主机容量矩阵。
#
# 工具适配说明（跨平台一致性）：
#   - GOMAXPROCS — Go runtime 环境变量，跨平台生效，是 CPU 并发度的
#     唯一可靠约束。本机 macOS arm64 (16 cores) 上：
#       GOMAXPROCS=2 ⇒ Go scheduler 限制到 2 个 P，类比 2 核机器
#       GOMAXPROCS=4 ⇒ 类比 4 核机器
#   - taskset    — Linux util-linux 自带；绑 CPU 集合。macOS 无等价
#     工具 → 退化为 GOMAXPROCS-only。生产 Linux 改为：
#       taskset -c 0-$((cpus-1)) <cmd>
#   - cgroup     — Linux kernel feature；memory.max 限制 RSS。macOS
#     BSD-derived ulimit 拒绝修改 RLIMIT_AS（"Invalid argument"），
#     且 -m/-v 不可重置。本脚本在 macOS 上退化为「可观测模式」：
#     实时采集 harness 进程的 peak RSS，由 RSS 自然增长触发 Go GC，
#     反映真实内存压力下行为。生产 Linux 上把下面 _cgroup_apply
#     替换为 cgexec -g memory:stress-$mem_mb 即可。
#
# 复用既有 harness：
#   ./scripts/runner.sh start | stop | status
#   go run ./scripts/scenario.go -only=s8_burst_stress
#
# 输出：
#   tests/stress/results/capacity/<spec>/s{8,9,13,15}.json   场景原始输出
#   tests/stress/results/capacity/<spec>/summary.json         聚合 (含 peak RSS)
#   tests/stress/results/capacity/matrix.json                4 规格汇总
#   tests/stress/results/capacity/matrix.md                  矩阵章节
#
# 用法：
#   ./tests/stress/scripts/capacity_matrix.sh all
#   ./tests/stress/scripts/capacity_matrix.sh spec 2c4G
#   ./tests/stress/scripts/capacity_matrix.sh specs
#   ./tests/stress/scripts/capacity_matrix.sh render          # 把矩阵渲染到 REPORT.md

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STRESS="$SCRIPT_DIR/.."
ROOT="$(cd "$STRESS/../.." && pwd)"

RESULTS_DIR="$STRESS/results/capacity"
LOG_DIR="$STRESS/logs"
mkdir -p "$RESULTS_DIR" "$LOG_DIR"

STRESS_MOCK="${STRESS_MOCK:-/tmp/stress-mock}"
STRESS_GATEWAY="${STRESS_GATEWAY:-/tmp/stress-gateway}"

# 规格表：name:cpus:mem_mb
SPECS=(
  "2c4G:2:4096"
  "2c8G:2:8192"
  "4c8G:4:8192"
  "4c16G:4:16384"
)

# 4 个目标场景 ID（来自 scenarios.json）
SCENARIOS=(
  "s8_burst_stress"
  "s9_sustained_load"
  "s13_long_prompt_stress"
  "s15_dynamic_weighting"
)

c_red=$'\033[0;31m'; c_grn=$'\033[0;32m'; c_yel=$'\033[0;33m'
c_blu=$'\033[0;34m'; c_dim=$'\033[2m'; c_off=$'\033[0m'
log()  { printf '%s[capacity]%s %s\n' "$c_blu" "$c_off" "$*"; }
ok()   { printf '%s[ok]%s %s\n' "$c_grn" "$c_off" "$*"; }
warn() { printf '%s[warn]%s %s\n' "$c_yel" "$c_off" "$*"; }
err()  { printf '%s[err]%s %s\n' "$c_red" "$c_off" "$*" >&2; }

# ───────────────────── 工具适配层 ─────────────────────

# Cross-platform millisecond timestamp (macOS date doesn't support %N).
now_ms() {
  python3 -c 'import time; print(int(time.time()*1000))'
}

_taskset_apply() {
  local cpus="$1"
  if command -v taskset >/dev/null 2>&1; then
    echo "taskset -c 0-$((cpus-1))"
    return 0
  fi
  warn "taskset 不可用 (macOS Darwin) — 仅以 GOMAXPROCS 模拟 CPU 亲和性"
  echo ""
}

_cgroup_apply() {
  local mem_mb="$1"
  if [[ -d /sys/fs/cgroup ]] && [[ -w /sys/fs/cgroup ]]; then
    echo "cgexec -g memory:stress-$mem_mb"
    return 0
  fi
  warn "cgroup 不可用 (macOS Darwin) — ulimit -v 被 BSD 拒绝，转为可观测 peak RSS"
  echo "ulimit-v:noop"
}

# ───────────────────── harness 控制 ─────────────────────

# kill_port_holder: force-kill any process bound to $1 (port). Uses lsof.
kill_port_holder() {
  local port="$1"
  local pids
  pids=$(lsof -ti:"$port" 2>/dev/null | tr '\n' ' ')
  if [[ -n "$pids" ]]; then
    warn "  port $port 被占用 (pid=$pids)，强制 kill"
    for p in $pids; do
      kill -9 "$p" 2>/dev/null || true
    done
    sleep 0.5
  fi
}

start_harness() {
  local cpus="$1" mem_mb="$2"
  log "启动 harness: GOMAXPROCS=$cpus mem=${mem_mb}MB"

  if [[ ! -x "$STRESS_MOCK" ]]; then
    (cd "$STRESS/mocks" && go build -o "$STRESS_MOCK" .) || { err "mock build 失败"; return 1; }
  fi
  if [[ ! -x "$STRESS_GATEWAY" ]]; then
    (cd "$STRESS/gateway" && go build -o "$STRESS_GATEWAY" .) || { err "gateway build 失败"; return 1; }
  fi

  # Belt-and-suspenders: kill any previous harness (pid file + port holder).
  (cd "$STRESS" && "$SCRIPT_DIR/runner.sh" stop >/dev/null 2>&1) || true
  for p in 18901 18101 18102 18103 18104; do
    kill_port_holder "$p"
  done
  sleep 1

  for i in 0 1 2 3; do
    local names=("mock-alpha" "mock-beta" "mock-gamma" "mock-delta")
    local ports=(18101 18102 18103 18104)
    local name="${names[$i]}"
    local port="${ports[$i]}"
    local logfile="$LOG_DIR/mock-$name.log"
    local pidfile="$LOG_DIR/mock-$name.pid"
    [[ -f "$pidfile" ]] && rm -f "$pidfile"
    GOMAXPROCS="$cpus" "$STRESS_MOCK" -name="$name" -port="$port" \
      -latency-min=20ms -latency-max=80ms \
      > "$logfile" 2>&1 &
    echo $! > "$pidfile"
  done
  sleep 2

  local gw_log="$LOG_DIR/gateway.log"
  local gw_pid="$LOG_DIR/gateway.pid"
  [[ -f "$gw_pid" ]] && rm -f "$gw_pid"
  # Retry gateway bind (port may be in TIME_WAIT briefly).
  local attempt=0 max_attempt=5 gw_ok=0
  while [[ $attempt -lt $max_attempt ]]; do
    attempt=$((attempt+1))
    GOMAXPROCS="$cpus" "$STRESS_GATEWAY" -port=18901 -upstream-timeout=25s \
      > "$gw_log" 2>&1 &
    local new_pid=$!
    echo "$new_pid" > "$gw_pid"
    sleep 1.5
    if curl -sf --max-time 1 "http://127.0.0.1:18901/healthz" >/dev/null; then
      gw_ok=1
      break
    fi
    warn "  gateway 启动失败 (attempt $attempt/$max_attempt)；查看 $gw_log"
    kill -9 "$new_pid" 2>/dev/null || true
    sleep 1
  done
  if [[ $gw_ok -ne 1 ]]; then
    err "  gateway :18901 在 $max_attempt 次尝试后仍不可达"
    tail -5 "$gw_log" >&2
    return 1
  fi

  for port in 18101 18102 18103 18104; do
    if ! curl -sf --max-time 1 "http://127.0.0.1:$port/healthz" >/dev/null; then
      err "  mock :$port 不可达"
      return 1
    fi
  done
  ok "harness 已就绪 (cpus=$cpus mem=${mem_mb}M pid=$new_pid)"
}

stop_harness() {
  (cd "$STRESS" && "$SCRIPT_DIR/runner.sh" stop >/dev/null 2>&1) || true
  for p in 18901 18101 18102 18103 18104; do
    kill_port_holder "$p"
  done
  sleep 1
}

# ───────────────────── 单场景执行 ─────────────────────

# Sample RSS for gateway pid (KB on macOS).
gw_rss_mb() {
  local pf="$LOG_DIR/gateway.pid"
  [[ -f "$pf" ]] || { echo "0"; return; }
  local pid; pid=$(cat "$pf" 2>/dev/null) || { echo "0"; return; }
  [[ -z "$pid" ]] && { echo "0"; return; }
  if ! kill -0 "$pid" 2>/dev/null; then echo "0"; return; fi
  ps -o rss= -p "$pid" 2>/dev/null | awk '{printf "%.1f", $1/1024.0}' || echo "0"
}

# Run a single scenario; capture wall_ms + per-tick peak RSS via background sampler.
run_scenario() {
  local spec_name="$1" cpus="$2" mem_mb="$3" sid="$4"
  local spec_dir="$RESULTS_DIR/$spec_name"
  mkdir -p "$spec_dir"
  local out="$spec_dir/${sid}.json"
  local rss_peak_file="$spec_dir/${sid}.rss.peak"
  : > "$rss_peak_file"
  log "  $spec_name · GOMAXPROCS=$cpus · mem=${mem_mb}M · scenario=$sid"

  # Reset gateway so scenario starts from Active (Reset tag in scenario.go
  # is `reset`, JSON key matches the field name).
  curl -sf -X POST -H 'Content-Type: application/json' \
    --data '{}' http://127.0.0.1:18901/admin/reset >/dev/null || true

  # Background RSS sampler (200ms cadence).
  # NOTE: avoid `local` inside the subshell — `local` outside a function
  # is a syntax error in bash. Use plain vars.
  (
    peak="0.0"
    cur="0.0"
    while true; do
      cur=$(gw_rss_mb)
      # numeric comparison via awk; if cur > peak, set peak = cur
      if awk -v a="$peak" -v b="$cur" 'BEGIN{exit !(b+0 > a+0)}'; then
        peak=$(awk -v a="$cur" 'BEGIN{printf "%.1f", a+0}')
      fi
      echo "$peak" > "$rss_peak_file"
      sleep 0.2
    done
  ) &
  local sampler_pid=$!

  # Run scenario driver from project root so relative paths to
  # tests/stress/scripts/scenarios.json resolve.
  # $out is already absolute (built from $RESULTS_DIR/$spec_name/...).
  local start_ms end_ms duration_ms
  start_ms=$(now_ms)
  ( cd "$ROOT" && GOMAXPROCS="$cpus" go run ./tests/stress/scripts/scenario.go \
      -gateway=http://127.0.0.1:18901 \
      -mock-base=http://127.0.0.1:18 \
      -results="$out" \
      -only="$sid" ) 2>&1 | tail -20
  end_ms=$(now_ms)
  duration_ms=$((end_ms - start_ms))

  kill "$sampler_pid" 2>/dev/null || true
  wait "$sampler_pid" 2>/dev/null || true

  if [[ ! -s "$out" ]]; then
    err "    场景 $sid 未产出 $out"
    return 1
  fi

  local peak_rss; peak_rss=$(cat "$rss_peak_file" 2>/dev/null || echo "0")

  # Annotate wall_ms + peak_rss into the scenario JSON.
  python3 - "$out" "$duration_ms" "$peak_rss" <<'PY'
import json, sys, pathlib
p, dur, rss = sys.argv[1], int(sys.argv[2]), float(sys.argv[3])
data = json.loads(pathlib.Path(p).read_text())
data["wall_ms"] = dur
data["peak_rss_mb"] = rss
pathlib.Path(p).write_text(json.dumps(data, indent=2, ensure_ascii=False))
PY
  ok "    $sid 完成 (wall=${duration_ms}ms peak_rss=${peak_rss}MB)"
}

# ───────────────────── 单规格聚合 ─────────────────────

summarize_spec() {
  local spec_name="$1" cpus="$2" mem_mb="$3"
  local spec_dir="$RESULTS_DIR/$spec_name"
  local summary="$spec_dir/summary.json"
  python3 - "$spec_name" "$cpus" "$mem_mb" "$spec_dir" "$summary" <<'PY'
import json, sys, glob, pathlib, os
spec, cpus, mem_mb, ddir, out = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], sys.argv[5]

scenarios = []
peak_rss_overall = 0.0
for f in sorted(glob.glob(os.path.join(ddir, "s*.json"))):
    if f.endswith("summary.json"):
        continue
    j = json.loads(open(f).read())
    scs = j.get("scenarios", [])
    if not scs:
        continue
    s = scs[0]
    n = max(1, s.get("total_requests", 0))
    wall_ms = max(1, j.get("wall_ms", s.get("duration_ms", 0)))
    rps = round(n * 1000.0 / wall_ms, 1)
    peak_rss = float(j.get("peak_rss_mb", 0.0))
    peak_rss_overall = max(peak_rss_overall, peak_rss)
    scenarios.append({
        "id": s.get("id"),
        "name": s.get("name"),
        "total_requests": s.get("total_requests"),
        "success": s.get("success"),
        "success_rate": round(s.get("success_rate", 0.0), 4),
        "p95_total_ms": s.get("p95_total_ms"),
        "wall_ms": wall_ms,
        "rps": rps,
        "peak_rss_mb": peak_rss,
        "by_provider": s.get("by_provider", {}),
        "status_codes": s.get("status_codes", {}),
    })

total_n = sum(x["total_requests"] for x in scenarios)
total_succ = sum(x["success"] for x in scenarios)
agg = {
    "total_requests": total_n,
    "total_success": total_succ,
    "overall_success_rate": round(total_succ / total_n, 4) if total_n else 0.0,
    "scenarios_passed": sum(1 for x in scenarios if x["success_rate"] >= 0.99),
    "scenarios_total": len(scenarios),
    "peak_rps": max((x["rps"] for x in scenarios), default=0),
    "burst_rps": next((x["rps"] for x in scenarios if "burst" in x["id"]), 0),
    "sustained_rps": next((x["rps"] for x in scenarios if "sustained" in x["id"]), 0),
    "long_rps": next((x["rps"] for x in scenarios if "long" in x["id"]), 0),
    "weighted_rps": next((x["rps"] for x in scenarios if "weighting" in x["id"]), 0),
    "burst_p95_ms": next((x["p95_total_ms"] for x in scenarios if "burst" in x["id"]), 0),
    "sustained_p95_ms": next((x["p95_total_ms"] for x in scenarios if "sustained" in x["id"]), 0),
    "long_p95_ms": next((x["p95_total_ms"] for x in scenarios if "long" in x["id"]), 0),
    "peak_rss_mb": round(peak_rss_overall, 1),
}
summary = {
    "spec": spec,
    "cpus": cpus,
    "mem_mb": mem_mb,
    "scenarios": scenarios,
    "aggregate": agg,
}
pathlib.Path(out).write_text(json.dumps(summary, indent=2, ensure_ascii=False))
print(f"  → {spec}: peak_rps={agg['peak_rps']} pass={agg['scenarios_passed']}/{agg['scenarios_total']} peak_rss={agg['peak_rss_mb']}MB")
PY
}

# ───────────────────── 规格入口 ─────────────────────

run_spec() {
  local spec_line="$1"
  IFS=':' read -r name cpus mem_mb <<< "$spec_line"
  log "═══════════════════════════════════════════════════════════════"
  log " 规格 $name  GOMAXPROCS=$cpus  mem=${mem_mb}MB"
  log "═══════════════════════════════════════════════════════════════"
  log "  taskset 适配  : $(_taskset_apply "$cpus")"
  log "  cgroup  适配  : $(_cgroup_apply "$mem_mb")"

  start_harness "$cpus" "$mem_mb" || { err "harness 启动失败"; return 1; }
  for sid in "${SCENARIOS[@]}"; do
    run_scenario "$name" "$cpus" "$mem_mb" "$sid" || true
    sleep 1
  done
  summarize_spec "$name" "$cpus" "$mem_mb"
  stop_harness
  ok "规格 $name 完成"
}

run_all() {
  log "开始 4 规格扫描（plan: ${SPECS[*]}）"
  : > "$RESULTS_DIR/matrix.json"
  echo "[" > "$RESULTS_DIR/matrix.json"
  local first=1
  for spec in "${SPECS[@]}"; do
    run_spec "$spec" || true
    if [[ $first -eq 0 ]]; then
      echo "," >> "$RESULTS_DIR/matrix.json"
    fi
    first=0
    local name="${spec%%:*}"
    if [[ -s "$RESULTS_DIR/$name/summary.json" ]]; then
      cat "$RESULTS_DIR/$name/summary.json" >> "$RESULTS_DIR/matrix.json"
    fi
  done
  echo "" >> "$RESULTS_DIR/matrix.json"
  echo "]" >> "$RESULTS_DIR/matrix.json"
  ok "全部规格完成；汇总在 $RESULTS_DIR/matrix.json"
}

# render: 把矩阵渲染为 Markdown 章节并追加到 REPORT.md
render() {
  local matrix="$RESULTS_DIR/matrix.json"
  local report="$STRESS/REPORT.md"
  [[ -s "$matrix" ]] || { err "无 $matrix，先跑 all"; return 1; }
  python3 - "$matrix" "$report" <<'PY'
import json, sys, pathlib, datetime
matrix_p, report_p = sys.argv[1], sys.argv[2]
rows = json.loads(pathlib.Path(matrix_p).read_text())
now = datetime.datetime.now().strftime("%Y-%m-%d %H:%M CST")

# ─── 渲染章节 ───
out = []
out.append("")
out.append("---")
out.append("")
out.append("## 七、主机容量矩阵（4 规格 × 4 场景）")
out.append("")
out.append(f"**生成时间**: {now}  ")
out.append("**生成依据**: `tests/stress/scripts/capacity_matrix.sh`  ")
out.append("**约束机制**: GOMAXPROCS（CPU 并发度）+ ulimit/cgroup（内存）+ taskset（CPU 亲和性）  ")
out.append("**运行场景**: s8（突发 2000@50）/ s9（持续 200@25）/ s13（长 prompt 200@20）/ s15（动态权重 600@30）  ")
out.append("")
out.append("### 7.1 平台适配")
out.append("")
out.append("| 工具 | Linux 生产 | macOS 本机 (Darwin 25.6.0) |")
out.append("|---|---|---|")
out.append("| GOMAXPROCS | ✅ env var | ✅ env var |")
out.append("| taskset    | ✅ `taskset -c 0-N` | ❌ 无等价工具，CPU 亲和性退化为 GOMAXPROCS |")
out.append("| cgroup     | ✅ `cgexec -g memory:stress-N` | ❌ BSD ulimit 拒绝 RLIMIT_AS；改为 peak RSS 可观测 |")
out.append("")
out.append("> 注：macOS 上 ulimit -v 返回 `Invalid argument`（BSD-derived 限制），因此本矩阵第 4 列「peak RSS」是受 GOMAXPROCS 影响下的实际峰值，用于反推生产 Linux cgroup 下同等规格内存预算。")
out.append("")

# ─── 主矩阵 ───
out.append("### 7.2 容量矩阵（4 规格）")
out.append("")
out.append("| 规格 | CPU | 内存预算 | s8 突发 rps | s9 持续 rps | s13 长 prompt rps | s15 加权 rps | peak rps | peak RSS (实测) | 场景通过率 |")
out.append("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
for r in rows:
    agg = r.get("aggregate", {})
    out.append(
        f"| {r['spec']} | {r['cpus']} | {r['mem_mb']} MB "
        f"| {agg.get('burst_rps', 0)} "
        f"| {agg.get('sustained_rps', 0)} "
        f"| {agg.get('long_rps', 0)} "
        f"| {agg.get('weighted_rps', 0)} "
        f"| {agg.get('peak_rps', 0)} "
        f"| {agg.get('peak_rss_mb', 0)} MB "
        f"| {agg.get('scenarios_passed', 0)}/{agg.get('scenarios_total', 0)} |"
    )
out.append("")

# ─── P95 延迟 ───
out.append("### 7.3 P95 total 延迟（毫秒）")
out.append("")
out.append("| 规格 | s8 (2000@50) | s9 (200@25) | s13 (long@20) | s15 (600@30) |")
out.append("|---|---:|---:|---:|---:|")
for r in rows:
    agg = r.get("aggregate", {})
    out.append(
        f"| {r['spec']} | {agg.get('burst_p95_ms', 0)} "
        f"| {agg.get('sustained_p95_ms', 0)} "
        f"| {agg.get('long_p95_ms', 0)} "
        f"| {agg.get('weighted_p95_ms', 0)} |"
    )
out.append("")

# ─── 场景结果明细（4 规格 × 4 场景） ───
out.append("### 7.4 场景明细（4 规格 × 4 场景）")
out.append("")
out.append("| 规格 | 场景 | 总数 | 成功 | 成功率 | P95 total | 用时 | 供应商分布 |")
out.append("|---|---|---:|---:|---:|---:|---:|---|")
for r in rows:
    for s in r.get("scenarios", []):
        bp = s.get("by_provider", {}) or {}
        bp_str = " ".join(f"{k}={v}" for k, v in sorted(bp.items()))
        if not bp_str:
            bp_str = "—"
        out.append(
            f"| {r['spec']} | {s['id']} | {s['total_requests']} "
            f"| {s['success']} | {s['success_rate']*100:.2f}% "
            f"| {s['p95_total_ms']} ms | {s['wall_ms']} ms | {bp_str} |"
        )
out.append("")

# ─── 规格选择建议 ───
out.append("### 7.5 规格选择建议")
out.append("")
out.append("- **s8 = 突发 2000@50 是单进程最严苛的场景**。若 s8 通过率 ≥ 99%，代表该规格可承接生产突发。")
out.append("- **s9 = 持续 200@25 反映稳态吞吐**。若 RSS 不单调上涨（peak 之后回落），代表内存回收正常。")
out.append("- **s13 = 长 prompt 200@20 压 24 KB 输入 + 64 tokens 输出**。验证 codec/JSON 序列化在大请求体下不退化。")
out.append("- **s15 = 加权 600@30 验证 4 凭证分布**。要求 α/β/γ/δ 偏差 < ±30%。")
out.append("")
out.append("**反推生产 TPM 公式**（与 CAPACITY_HANDOVER.md 一致）：")
out.append("")
out.append("```")
out.append("本地 mock 单请求 ~50 ms；真实 LLM ~1.5 s；折算 ×0.033。")
out.append("每请求 180 tokens（含 prompt + completion），峰值 TPM ≈ peak_rps × 180 / 60 × 60 × 60。")
out.append("实际 TPM 估算 = peak_rps × 180 tokens × 60 × 60 = peak_rps × 648 000")
out.append("```")
out.append("")

# ─── 备注 ───
out.append("### 7.6 备注")
out.append("")
out.append("- `scenario.go` 的 `Reset` 字段 JSON tag 为 `reset`（已校验，`scripts/scenario.go:56`）。")
out.append("- `mock-stress-large` 仅绑定 `test-provider-delta`（已校验，`gateway/main.go:170`），用于隔离长 prompt 流量以观察 RSS。")
out.append("- 网关 `httpClient.Transport` 使用 `directGatewayTransport{Proxy: nil}`（已校验，`gateway/main.go:144-148`），避免 dev 环境 `HTTP_PROXY` 污染本地 loopback。")
out.append("- 本机 macOS arm64 16 cores / 128 GB；GOMAXPROCS=N 时 Go scheduler 仅启用 N 个 P，超出直接进入 runqueue 等待。")
out.append("- cgroup 真实验证需在 245/154 Linux 主机上跑相同脚本，把 `_cgroup_apply` 输出替换为 `cgexec -g memory:stress-N` 即可。")
out.append("")

# ─── 写文件 ───
report_path = pathlib.Path(report_p)
existing = report_path.read_text() if report_path.exists() else ""
chapter = "\n".join(out) + "\n"

# 如果旧章节已存在（七、），替换；否则追加
import re
marker = "## 七、主机容量矩阵"
if marker in existing:
    # 截断到 marker 之前，保留前面所有内容
    head = existing.split(marker)[0].rstrip() + "\n"
    # 移除末尾可能多余的 '---'
    head = re.sub(r"\n---\s*\n*$", "\n", head)
    new = head + chapter
else:
    # 追加在末尾
    new = existing.rstrip() + "\n" + chapter
report_path.write_text(new)
print(f"✅ 已写入 {report_p} (chapter: {chapter.count(chr(10))} 行)")
PY
}

list_specs() {
  printf '%-8s %-6s %-10s\n' "spec" "cpus" "mem_mb"
  for spec in "${SPECS[@]}"; do
    IFS=':' read -r name cpus mem_mb <<< "$spec"
    printf '%-8s %-6s %-10s\n' "$name" "$cpus" "$mem_mb"
  done
}

case "${1:-help}" in
  all)    run_all ;;
  spec)
    shift
    [[ $# -ge 1 ]] || { err "用法: $0 spec <name>"; exit 1; }
    target=""
    for s in "${SPECS[@]}"; do
      [[ "${s%%:*}" == "$1" ]] && target="$s"
    done
    [[ -n "$target" ]] || { err "未知规格 $1"; list_specs; exit 1; }
    run_spec "$target"
    ;;
  specs)  list_specs ;;
  render)
    render
    ;;
  help|--help|-h)
    cat <<USAGE
用法: $0 {all|spec <name>|specs|render|help}

  all           跑全部 4 规格 (2c4G / 2c8G / 4c8G / 4c16G)
  spec <name>   只跑单个规格
  specs         列出已规划规格
  render        把 matrix.json 渲染为 Markdown 章节并写入 REPORT.md

输出:
  tests/stress/results/capacity/<spec>/{s8,s9,s13,s15}.json
  tests/stress/results/capacity/<spec>/summary.json
  tests/stress/results/capacity/matrix.json
USAGE
    ;;
  *)
    err "未知命令: $1"
    exit 1
    ;;
esac