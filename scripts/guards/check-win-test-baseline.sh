#!/usr/bin/env bash
# =====================================================================
# scripts/guards/check-win-test-baseline.sh — Windows 全量测试基线门
# =====================================================================
# 判据（scripts/guards/win-test-baseline.json 是唯一事实源）：
#   实测失败集 − baseline = 新增失败 ⇒ exit 1（红，逐条报出）
#   baseline − 实测失败集 = 基线过宽（可收紧）⇒ exit 0 但逐条提示（棘轮只降不升）
#
# 用法：
#   bash scripts/guards/check-win-test-baseline.sh <go-test 输出日志>
#   bash scripts/guards/check-win-test-baseline.sh          # 无参 = 自己跑全量
#
# 日志格式 = `go test ./...` 的 stdout（FAIL\t<pkg> 行 + --- FAIL: 行）。
# 解析规则与日志的真实结构对齐：
#   · 每个包的 --- FAIL 行出现在该包 `FAIL\t<pkg>` 行**之前**（go test 按包
#     流式输出，包结果行是包的最后一行）⇒ 先累积 pending 名单，遇到包结果行
#     再归属；
#   · `ok\t<pkg>` 清空 pending（并行运行下包之间会交错，pending 只在包结果
#     行处结算，交错不影响归属）；
#   · 包级 FAIL 且无任何具名失败 ⇒ 记入 package_level（admin/bg 的
#     「全量并行跑才红、单包复跑 ok」形态）。
#
# 本门只认「名单」，不猜原因：环境类失败（symlink 特权/路径分隔符/9009）
# 与结构类失败（FIXME 失效/未登记面）都在 baseline 里逐条列名——
# 修好任何一个 ⇒ 门提示「可收紧」，把行删掉即可（棘轮只降不升）。
# =====================================================================
set -euo pipefail

# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
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

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASELINE="$ROOT_DIR/scripts/guards/win-test-baseline.json"
LOG="${1:-}"

command -v python >/dev/null 2>&1 || { echo "error: python 不可用（解析用）" >&2; exit 2; }
[[ -f "$BASELINE" ]] || { echo "error: baseline 不在：$BASELINE" >&2; exit 2; }

if [[ -z "$LOG" ]]; then
  echo "[win-test-baseline] 未给日志参数 ⇒ 本地跑全量 go test ./...（GOFLAGS=-p=2 防并行 OOM）..."
  LOG="$(mktemp)"
  trap 'rm -f "$LOG"' EXIT
  GOFLAGS="${GOFLAGS:--p=2}" go test ./... >"$LOG" 2>&1 || true
fi
[[ -s "$LOG" ]] || { echo "error: 日志为空：$LOG" >&2; exit 2; }

python - "$LOG" "$BASELINE" <<'PYEOF'
import json, re, sys

log_path, baseline_path = sys.argv[1], sys.argv[2]
baseline = json.load(open(baseline_path, encoding='utf-8'))
base_pkgs = {p: set(v) for p, v in baseline.get('packages', {}).items()}
base_level = set(baseline.get('package_level', []))

lines = open(log_path, encoding='utf-8', errors='replace').read().splitlines()
observed = {}          # pkg -> set(test names)
observed_level = []    # package-level FAIL with no named tests
pending = []

for ln in lines:
    m = re.match(r'^\s*--- FAIL: ([A-Za-z0-9_/]+)', ln)
    if m:
        pending.append(m.group(1))
        continue
    m = re.match(r'^(ok|FAIL)\tgithub\.com/kaixuan/llm-gateway-go/(\S+)', ln)
    if m:
        pkg, status = m.group(2), m.group(1)
        if status == 'FAIL':
            if pending:
                observed.setdefault(pkg, set()).update(pending)
            else:
                observed_level.append(pkg)
        pending = []

# bare trailing FAIL（汇总行）不携带信息，忽略。

new_failures = []      # 实测有、baseline 无 ⇒ 红
for pkg, tests in sorted(observed.items()):
    known = base_pkgs.get(pkg, set())
    for t in sorted(tests - known):
        new_failures.append(f"{pkg} :: {t}")
for pkg in observed_level:
    if pkg not in base_level and not observed.get(pkg):
        new_failures.append(f"{pkg} :: <package-level>")

stale = []             # baseline 有、实测没有 ⇒ 可收紧提示
for pkg, tests in sorted(base_pkgs.items()):
    got = observed.get(pkg, set())
    for t in sorted(tests - got):
        # 该包本轮实测为具名失败集 ⇒ 缺的条目就是过宽；包根本没出现在日志
        # （构建跳过等）⇒ 无法判定，不提示也不报红（如实记）。
        if pkg in observed:
            stale.append(f"{pkg} :: {t}")
for pkg in sorted(base_level):
    if pkg in observed_level:
        continue
    if pkg in observed or any(k.startswith(pkg + '/') for k in observed):
        stale.append(f"{pkg} :: <package-level> （本轮未包级失败）")

print(f"[win-test-baseline] 实测失败包 {len(observed)} 个 / 包级 {len(observed_level)} 个；"
      f"baseline 记 {len(base_pkgs)} 包 {sum(len(v) for v in base_pkgs.values())} 测试 + 包级 {len(base_level)}")

if new_failures:
    print(f"[win-test-baseline] FAIL：{len(new_failures)} 条新增失败（不在 baseline）⇒ 修测试或修产品后重跑；"
          f"确属环境新形态则把条目加进 {baseline_path} 并写明理由：")
    for x in new_failures:
        print("  NEW " + x)
    sys.exit(1)

if stale:
    print(f"[win-test-baseline] 提示：{len(stale)} 条 baseline 条目本轮未复现（棘轮只降不升，可收紧）：")
    for x in stale:
        print("  STALE " + x)

print("[win-test-baseline] PASS：实测失败集 ⊆ baseline，零新增失败")
PYEOF
