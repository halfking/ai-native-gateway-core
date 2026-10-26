#!/usr/bin/env bash
# matrix-verdict-crosscheck.sh —— 「观测覆盖自检」接入矩阵读数的**薄封装**（v2）
#
# ★ 它做什么（★ 不修改矩阵脚本本身 —— 那是并行会话的文件）：
#   1. 解析【矩阵脚本 stdout 的读数段】⇒ 口径 A：每档的「合计」（= 矩阵自己数到的 verdict 数）
#   2. 跑【覆盖自检器 check-verdict-coverage.py】⇒ 口径 B：每档的「期望函数数」
#   3. 把两份口径交给【双口径对拍器 two-caliber-crosscheck.py】⇒ 不一致 ⇒ 拒绝出读数
#   4. ★★ v2 新增：「口径 D 零漂移」对拍（探针 E 桶 vs 官方门本体计数）
#
# ★★ v2 相对 v1 的两处修改（★★ 都是 R97 轮被上游改动**实测打断**之后改的）：
#
#   ① 读数段解析：从「**固定列数**的位置匹配」改成「**表头锚定**」
#      ⇒ 起因：上游 R96 轮给读数表**加了一列**（「未观测」）⇒ v1 的
#        `^([A-Z][0-9]+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)$` 直接失配。
#      ⇒ ⇒ v1 的失效模式是 **fail-closed**（exit 3「拒绝出读数」）★ **方向是对的**
#        （不是「静默读出错的数」）⇒ ⇒ 但「上游加一列就红」本身是**脆弱点**。
#      ⇒ ⇒ 修法：锚在【语义确切的那一列】上 —— 找表头里叫「合计」的那列，
#        再按**列位置**取值 ⇒ ⇒ 上游加列 / 调列序都不再打断它。
#      ⇒ ⇒ ★ 判据纪律：「防假阳性」的最有效手段是**把标记收窄到语义确切的那一个**，
#        不是加上下文启发（此处「合计」就是那个语义确切的一列）。
#
#   ② 新增「口径 D 零漂移」对拍：
#      ⇒ 上游脚本的口径 D（结论口径）由一个**运行时生成的探针**算出，
#        探针**直接调用官方门的未导出函数**（dsnNameLiterals / opensPostgresConn）
#        ⇒ ⇒ **判据原语**层零漂移（真的共用同一份实现）★ 这点核实过，成立。
#      ⇒ ⇒ 但探针**自带一份「文件选取」副本**（walk + skip 列表 + 只排除自己）
#        ⇒ ⇒ ★★ **「选取」层并不零漂移** ⇒ ⇒ 官方门一改（加一个排除 / 改 skip 列表），
#        探针不会跟着改 ⇒ ⇒ 口径 D 会**静默漂移**，而矩阵读数照常发布。
#      ⇒ ⇒ ★ 这条「零漂移」承诺今天**恰好成立**（skip 列表一致 + 探针自己不是 PG-opener
#        ⇒ 两边数到的 PG 文件数应当相等），但**没有任何东西守着它**。
#      ⇒ ⇒ ⇒ v2 把它变成**可对拍的两个独立口径**：
#          口径 A′ = 探针 E 桶的文件数（探针日志里的 `PROBE-CAL E files=N`）
#          口径 B′ = 官方门**本体**数到的 PG 文件数（官方门自己的 log 行）
#        ⇒ ⇒ 两边由**不同代码路径**各自算出 ⇒ 不等 ⇒ 漂移 ⇒ 拒绝出读数。
#
# ★★ 为什么对拍的必须是【合计】对【期望函数数】，而不是【合计】对【被覆盖函数数】：
#   ★ 矩阵的「合计」与覆盖自检器的「被覆盖函数数」是**同一个量的两次实现**
#     ⇒ 对拍它们只能证明「两个实现一致」⇒ ★ 覆盖有缺口时（87 vs 91）**两个数一起变小**
#     ⇒ ⇒ 两侧仍然一致 ⇒ ⇒ **不会报警** ⇒ ⇒ ⇒ 那是自证，不是对拍。
#   ★★ 真正的报警器是：【矩阵说它跑了 N 个】对【期望清单有 M 个】⇒ ★ N ≠ M ⇒ 覆盖有缺口
#     ⇒ ⇒ 而 N == M **同时**交叉验证了两个实现（矩阵的聚合 vs 覆盖自检的计数）⇒ 一石二鸟。
#   ⇒⇒⇒ ⇒ **⇒ 这就是「两个独立口径 + 不一致即拒绝出读数」在矩阵上的正确接法。**
#
# 用法:
#   matrix-verdict-crosscheck.sh --matrix-log <矩阵 stdout 文件> --out <矩阵输出目录>
#                                 [--probe-log <探针日志>] [--gate-log <官方门日志>]
#                                 [--wt <矩阵 worktree 目录>]
#
#   --probe-log  探针日志（默认取 <out>/scope_probe.log；给了却不存在 ⇒ 输入无效）
#   --gate-log   官方门日志（已有就直接用）
#   --wt         ★ 给出 worktree ⇒ 本封装**自己去跑官方门**并取它的计数
#                ⇒ ★ 这是「零漂移对拍」的**可复跑**入口（不靠人工粘贴日志）
#
# 退出码（★ 多段判据时的**优先级**：无效(3) > 不一致(2) > 一致(0)）：
#   0  全部判据一致（或被显式 SKIP 且说清理由）⇒ 可以给结论
#   2  ★ 至少一段不一致 ⇒ **拒绝出读数**
#   3  ★ 输入无效（矩阵日志缺失 / 读数段解析不出 / 覆盖自检器跑不动 /
#      零漂移对拍被要求但拿不到可比读数）⇒ 拒绝出结论
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECKER="$REPO_ROOT/scripts/audit/check-verdict-coverage.py"
CROSSCHECK="$REPO_ROOT/scripts/audit/two-caliber-crosscheck.py"

RC=0
note_invalid() { echo "★★ 【判据失败·输入无效】$1" >&2; RC=3; }
# ★ 早退路径也必须打【最终裁决·输入无效】⇒ ⇒ 否则调用方/门分不清「从哪条路拒的」
die_invalid() { echo "★★ 【判据失败·输入无效】$1" >&2
           echo "★★ 【最终裁决·输入无效】$1" >&2
           exit 3; }
note_mismatch() { echo "★★ 【判据失败·不一致】$1" >&2; [ "$RC" -eq 0 ] && RC=2; }

MATRIX_LOG=""
OUT_DIR=""
PROBE_LOG=""
GATE_LOG=""
WT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --matrix-log) MATRIX_LOG="$2"; shift 2;;
    --out) OUT_DIR="$2"; shift 2;;
    --probe-log) PROBE_LOG="$2"; shift 2;;
    --gate-log) GATE_LOG="$2"; shift 2;;
    --wt) WT="$2"; shift 2;;
    *) echo "★★ 【最终裁决·输入无效】未知参数: $1" >&2; exit 3;;
  esac
done

if [ -z "$MATRIX_LOG" ] || [ -z "$OUT_DIR" ]; then
  die_invalid "必填：--matrix-log <矩阵 stdout> --out <矩阵输出目录>"
fi
if [ ! -f "$MATRIX_LOG" ]; then
  die_invalid "矩阵 stdout 文件不存在: $MATRIX_LOG"
fi
if [ ! -f "$OUT_DIR/scope.tsv" ]; then
  die_invalid "矩阵输出目录里没有 scope.tsv: $OUT_DIR"
fi
if [ ! -f "$CHECKER" ] || [ ! -f "$CROSSCHECK" ]; then
  die_invalid "依赖缺失：$CHECKER / $CROSSCHECK"
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/matrix-xcheck.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT


# ── 判据 1：口径 A = 矩阵读数段的「合计」（★ 表头锚定解析）────────────────────
echo "== 判据 1：矩阵合计 vs 期望函数数 =="
python3 - "$MATRIX_LOG" "$WORK/caliber_matrix.tsv" <<'PY'
import re, sys

log_path, out_path = sys.argv[1], sys.argv[2]
# ★★ 表头锚定：先找到**含「合计」那一列**的表头行，再按列位取值。
#   ⇒ 上游加列 / 调列序都不再打断本解析（v1 的固定 4 列正则在 R96 加列后失配）。
HEADER_KEY = "合计"
ARCH_RE = re.compile(r"^[A-Z][0-9]+$")

lines = []
with open(log_path, errors="replace") as f:
    lines = f.read().splitlines()

hdr_idx, hdr_cols = -1, []
for i, line in enumerate(lines):
    toks = line.split()
    if HEADER_KEY in toks:
        hdr_idx, hdr_cols = i, toks
        break
if hdr_idx < 0:
    sys.stderr.write("!! 矩阵 stdout 里找不到含「%s」的表头行 ⇒ 读数段形态已变 ⇒ 拒绝出读数\n"
                     % HEADER_KEY)
    sys.exit(3)
col = hdr_cols.index(HEADER_KEY)

rows = []
for line in lines[hdr_idx + 1:]:
    if not line.strip():
        continue
    toks = line.split()
    if not (ARCH_RE.match(toks[0]) and len(toks) > col):
        break                      # 读数表是连续的 ⇒ 遇到非数据行即止
    if not all(t.isdigit() for t in toks[1:]):
        break
    rows.append((toks[0], int(toks[col])))
if not rows:
    sys.stderr.write("!! 表头之后解析不出任何读数行 ⇒ 拒绝出读数\n")
    sys.exit(3)

with open(out_path, "w", encoding="utf-8") as f:
    for arch, total in rows:
        f.write("%s\t%d\n" % (arch, total))
print("  口径 A（矩阵合计）: " + ", ".join("%s=%d" % r for r in rows)
      + "   [表头列位 %d: %s]" % (col, " ".join(hdr_cols)))
PY
if [ $? -ne 0 ]; then die_invalid "读数段解析失败（表头形态已变或读数段缺失）"; fi

# ── 判据 2：口径 B = 覆盖自检器的「期望函数数」 ─────────────────────────────────
echo "== 判据 2：期望函数数 =="
python3 - "$OUT_DIR" "$WORK/caliber_coverage.tsv" "$CHECKER" <<'PY'
import json, os, subprocess, sys
out_dir, out_path, checker = sys.argv[1], sys.argv[2], sys.argv[3]
if not os.path.exists(checker):
    sys.stderr.write("!! 覆盖自检器不存在: %s\n" % checker)
    sys.exit(3)
scope = os.path.join(out_dir, "scope.tsv")
arches = ["A1", "B1", "B2"]
rows = []
for arch in arches:
    p = subprocess.run(["python3", checker, "--scope", scope, "--logs", out_dir,
                        "--arch", arch, "--json"], capture_output=True, text=True)
    try:
        rep = json.loads(p.stdout)
    except Exception:
        sys.stderr.write("!! 覆盖自检器在 %s 档没给出可解析的 JSON（rc=%s）\n%s\n"
                         % (arch, p.returncode, p.stdout[-500:]))
        sys.exit(3)
    rows.append((arch, int(rep.get("expected_functions", 0)), int(rep.get("covered_functions", 0)),
                 int(rep.get("packages", 0)), p.returncode))
with open(out_path, "w", encoding="utf-8") as f:
    for arch, exp, cov, pkgs, rc in rows:
        f.write("%s\t%d\n" % (arch, exp))
print("  口径 B（期望函数数）: " + ", ".join("%s=%d" % (r[0], r[1]) for r in rows))
for arch, exp, cov, pkgs, rc in rows:
    print("    %s: 包 %d / 期望 %d / 已覆盖 %d / 覆盖自检退出码 %d%s"
          % (arch, pkgs, exp, cov, rc, "" if rc == 0 else "  ← ★ 该档覆盖不完整"))
PY
if [ $? -ne 0 ]; then die_invalid "覆盖自检器跑不动（拿不到期望函数数）"; fi

# ── 对拍 1：矩阵合计 vs 期望函数数 ─────────────────────────────────────────────
echo "  对拍：矩阵合计 vs 期望函数数"
python3 "$CROSSCHECK" \
  --a "$WORK/caliber_matrix.tsv" \
  --b "$WORK/caliber_coverage.tsv" \
  --key-cols 0 --value-col 1 \
  --label-a "矩阵合计" --label-b "覆盖期望函数数" \
  --independent
rc=$?
if [ "$rc" -eq 0 ]; then
  echo "✅ 判据 1 一致 ⇒ 三档的「矩阵合计」与「期望函数数」全部相等 ⇒ 覆盖完整"
  echo "★ 诚实边界：一致只证明「两个独立口径对同一总体给出一致的计数」⇒ 不证明「这些判定正确」"
elif [ "$rc" -eq 2 ]; then
  note_mismatch "判据 1 不一致 ⇒ **拒绝出读数**（矩阵自己的汇总会把不一致的那档印成 0 并据此报出假变化）"
else
  note_invalid "判据 1 的输入无效 ⇒ 拒绝出结论"
  exit 3
fi

# ── 判据 3：★「口径 D 零漂移」对拍（探针 E 桶 vs 官方门本体计数）───────────────
echo
echo "== 判据 3：口径 D 零漂移（探针 E 桶 vs 官方门本体） =="
if [ -z "$PROBE_LOG" ] && [ -f "$OUT_DIR/scope_probe.log" ]; then
  PROBE_LOG="$OUT_DIR/scope_probe.log"
fi
DRIFT_ENABLED=0
if [ -n "$GATE_LOG" ] || [ -n "$WT" ]; then
  DRIFT_ENABLED=1
fi
if [ "$DRIFT_ENABLED" -eq 0 ]; then
  echo "⏭  【判据3·SKIP】未给 --gate-log / --wt ⇒ 本次不做零漂移对拍"
  echo "    ★ 理由：零漂移对拍需要**官方门本体**的计数 ⇒ 没有门日志就没有第二个独立口径"
  echo "    ★ 要做：加 --wt <矩阵 worktree>（本封装会自己去跑官方门）或 --gate-log <已有日志>"
else
  # 3a. 门日志：--wt ⇒ 本封装自己跑官方门；否则用 --gate-log 给的
  if [ -n "$WT" ]; then
    GATE_LOG="$WORK/gate.log"
    echo "  在 ${WT} 里跑官方门（只要它的 PG 文件计数）…"
    ( cd "$WT" && go test ./scripts/ursmcheck/ \
        -run '^TestRealDBLegsUseDeclaredDSNEnvNames$' -count=1 -v ) >"$GATE_LOG" 2>&1
    echo "  官方门退出码 = $?（★ 门自己红不红与本对拍无关；这里只要它的计数行）"
  fi
  if [ ! -f "$GATE_LOG" ]; then
    die_invalid "零漂移对拍被要求，但官方门日志不存在: $GATE_LOG ⇒ 拿不到第二个独立口径 ⇒ 拒绝出结论"
  fi
  # 3b. 两个口径各写一份 TSV，再交给对拍器
  python3 - "$PROBE_LOG" "$GATE_LOG" "$WORK/drift_probe.tsv" "$WORK/drift_gate.tsv" <<'PY'
import re, os, sys
probe_log, gate_log, out_probe, out_gate = sys.argv[1:5]
if not os.path.exists(probe_log):
    sys.stderr.write("!! 探针日志不存在: %s\n" % probe_log)
    sys.exit(3)
if not os.path.exists(gate_log):
    sys.stderr.write("!! 官方门日志不存在: %s\n" % gate_log)
    sys.exit(3)

# 口径 A′ = 探针 E 桶（官方门自己的分母：全部打开 PG 的 _test.go）
a = None
with open(probe_log, errors="replace") as f:
    for line in f:
        m = re.search(r"PROBE-CAL E files=(\d+)", line)
        if m:
            a = int(m.group(1))
            break
if a is None:
    sys.stderr.write("!! 探针日志里找不到「PROBE-CAL E files=N」⇒ 探针没跑成功 ⇒ 拒绝出读数\n")
    sys.exit(3)

# 口径 B′ = 官方门本体数到的 PG 文件数（官方门自己的 log 行）
b = None
with open(gate_log, errors="replace") as f:
    for line in f:
        m = re.search(r"扫到 (\d+) 个打开 PG 的测试文件", line)
        if m:
            b = int(m.group(1))
            break
if b is None:
    sys.stderr.write("!! 官方门日志里找不到「扫到 N 个打开 PG 的测试文件」⇒ 官方门没给出可比读数\n")
    sys.exit(3)

with open(out_probe, "w", encoding="utf-8") as f:
    f.write("PG文件数\t%d\n" % a)
with open(out_gate, "w", encoding="utf-8") as f:
    f.write("PG文件数\t%d\n" % b)
print("  口径 A′（探针 E 桶）= %d" % a)
print("  口径 B′（官方门本体）= %d" % b)
PY
  if [ $? -ne 0 ]; then
    die_invalid "零漂移对拍拿不到可比读数 ⇒ 拒绝出结论"
  fi
  python3 "$CROSSCHECK" \
    --a "$WORK/drift_probe.tsv" \
    --b "$WORK/drift_gate.tsv" \
    --key-cols 0 --value-col 1 \
    --label-a "探针E桶(口径D的walk副本)" --label-b "官方门本体(官方口径)" \
    --independent
  rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "✅ 判据 3 一致 ⇒ 探针的「文件选取」与官方门本体数到同样的 PG 文件数 ⇒ 「零漂移」今天成立"
  elif [ "$rc" -eq 2 ]; then
    note_mismatch "判据 3 不一致 ⇒ 探针的「文件选取」已与官方门本体漂移 ⇒ 口径 D 不再零漂移 ⇒ **拒绝出读数**"
  else
    note_invalid "判据 3 的输入无效 ⇒ 拒绝出结论"
    exit 3
  fi
fi

echo
if [ "$RC" -eq 0 ]; then
  echo "✅ 【最终裁决·一致】全部判据通过 ⇒ 可以给结论"
  exit 0
fi
if [ "$RC" -eq 2 ]; then
  echo "★★ 【最终裁决·不一致】至少一段不一致 ⇒ **拒绝出读数**"
  exit 2
fi
echo "★★ 【最终裁决·输入无效】输入无效 ⇒ 拒绝出结论"
exit 3
