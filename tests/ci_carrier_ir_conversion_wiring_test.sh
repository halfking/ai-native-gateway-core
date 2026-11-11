#!/usr/bin/env bash
# ci_carrier_ir_conversion_wiring_test.sh —— CI 载体接线门（2026-11-11 IR 协议转换层审计轮）
#
# ── 本门存在的根因 ──────────────────────────────────────────────────────
# 本仓远端是 codeup.aliyun.com ⇒ **`.github/workflows/` 不在执行路径上**
# ⇒ 2026-11-11 IR 协议转换层审计轮把三个变异 harness 接进了
#   `.github/workflows/ir-protocol-conversion-ci.yml`
#   ⇒ ★★ **接到了不会执行的载体上** ⇒ ★★ 「接线了却跑不到」
#   ⇒ ★★ 按本仓纪律「门没有调用方 = 声明不是门」⇒ 那三个 harness 此前
#     **不是门，是文件**（而且没人会发现，因为没人看那个目录）。
#
# ── 为什么已有的载体真实性门抓不到 ──────────────────────────────────────
# 仓内 `scripts/ursmcheck/ci_carrier_truth_test.go`（C1/C2/C3）判的是
# 「**载体里有没有门**」⇒ ★★ **它抓不到「某个具体的门没接线」**
# ⇒ 且该 Go 门住在 `scripts/ursmcheck` ⇒ 属 **GUARD_PACKAGES_SLOW**
#   ⇒ ★★ **CI 快门（guards-fast）根本不跑它** ⇒ 连「载体自身」都没在 CI 上自证。
# ⇒ 本门因此做成 **shell 门**并挂进 `guards-gate-scripts`（⇒ 进 CI 快门）。
#
# ── 本门钉的【链路】不变量 ─────────────────────────────────────────────
#   ① `.workflow/` 下至少有一个流水线配置（读不到 ⇒ fail-closed，不静默跳过）
#   ② 每个流水线配置里有**非注释行**调用 `make guards-fast`
#   ③ `Makefile` 的 `guards-fast` 与 `guards` 两个 target 的前置里都含
#      `guards-ir-conversion`（IR 门必须在**真正在跑的载体**链路上）
#   ④ `guards-ir-conversion` 的配方里逐个调用了那三个变异 harness 脚本
#   ⑤ 三个 harness 脚本本身存在
#
# ★ 变异臂：合成夹具证明「非注释行判定」真的有牙（把 `make guards-fast`
#   挪进注释 ⇒ 必须被判红）⇒ ★ 否则本门可能恒绿（量具自检）。
#
# 纪律：bash 3.2 可跑（macOS 自带 bash）★ 零外部依赖 ★ 读不到判据 ⇒ fail-closed。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAKEFILE="$ROOT/Makefile"
WORKFLOW_DIR="$ROOT/.workflow"

# IR 门在载体链路上的入口 target 及其配方必须调用的三个 harness。
IR_TARGET="guards-ir-conversion"
IR_HARNESSES="ir-client-error-fix-mutation.sh ir-gemini-only-loss-mutation.sh ir-coerce-numeric-mutation.sh"

pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  ✅ %s\n' "$*"; }
bad() { fail=$((fail+1)); printf '  ❌ %s\n' "$*"; }

# ── 取 Makefile 某个 target 的前置列表（只认行首精确匹配，不吃前缀）────
# ★ 例：`guards:` 不得被 `guards-fast:` 命中 ⇒ 判据锚在 `^target[ \t]*:`。
target_prereqs() {
  awk -v t="$1" '$0 ~ "^" t "[ \t]*:" { sub("^" t "[ \t]*:[ \t]*", ""); print; exit }' "$2"
}

# ── 取 Makefile 某个 target 的配方（make 配方行以 TAB 开头）────────────
target_recipe() {
  awk -v t="$1" '
    $0 ~ "^" t "[ \t]*:" { inrec=1; next }
    inrec {
      if (substr($0,1,1) != "\t") exit
      print
    }' "$2"
}

# ── 载体里有没有**非注释行**调 `make guards-fast` ───────────────────────
# ★ 注释行（`#` 开头，缩进后带 # 的也算）不算 ⇒ 「注释里写不算」。
carrier_calls_guards_fast() {
  local wfdir="$1" f
  [[ -d "$wfdir" ]] || return 1
  for f in "$wfdir"/*.yml "$wfdir"/*.yaml; do
    [[ -f "$f" ]] || continue
    # ^[^#]* ⇒ 不允许跨过注释符；因此 `# ... make guards-fast` 不会被命中。
    if grep -qE '^[^#]*\bmake[[:space:]]+guards-fast\b' "$f"; then
      return 0
    fi
  done
  return 1
}

# ══ 判据 ①：载体目录存在且至少有一个流水线配置（fail-closed）══════════
configs=""
if [[ -d "$WORKFLOW_DIR" ]]; then
  for f in "$WORKFLOW_DIR"/*.yml "$WORKFLOW_DIR"/*.yaml; do
    [[ -f "$f" ]] && configs="$configs $(basename "$f")"
  done
fi
if [[ -n "${configs// /}" ]]; then
  ok "载体目录存在且有流水线配置：${configs# }"
else
  bad "`.workflow/` 下找不到任何流水线配置 ⇒ 在跑的载体不存在 ⇒ 判据失效（fail-closed）"
fi

# ══ 判据 ②：载体里有非注释行调 `make guards-fast` ═════════════════════
if carrier_calls_guards_fast "$WORKFLOW_DIR"; then
  ok "在跑的载体里有**非注释行**调用 make guards-fast"
else
  bad "在跑的载体里没有非注释行调用 make guards-fast ⇒ 守卫在 CI 上是空转的"
fi

# ══ 判据 ③：IR 门在两个 target 的前置里 ═══════════════════════════════
if [[ ! -f "$MAKEFILE" ]]; then
  bad "Makefile 不存在 ⇒ 判据失效（fail-closed）"
else
  for t in guards-fast guards; do
    prereqs="$(target_prereqs "$t" "$MAKEFILE")"
    if printf '%s' "$prereqs" | grep -qw "$IR_TARGET"; then
      ok "Makefile 的 \`$t\` 前置里含 $IR_TARGET"
    else
      bad "Makefile 的 \`$t\` 前置里**没有** $IR_TARGET ⇒ IR 门不在$([ "$t" = "guards-fast" ] && echo 'CI 快门（真正在跑的载体入口）' || echo '本地全量门')链路上"
    fi
  done

  # ══ 判据 ④：配方里逐个调用三个 harness ═══════════════════════════
  recipe="$(target_recipe "$IR_TARGET" "$MAKEFILE")"
  if [[ -z "${recipe// /}" ]]; then
    bad "Makefile 里找不到 $IR_TARGET 的配方 ⇒ 判据失效（fail-closed）"
  else
    for h in $IR_HARNESSES; do
      if printf '%s\n' "$recipe" | grep -qF "$h"; then
        ok "$IR_TARGET 配方调用了 $h"
      else
        bad "$IR_TARGET 配方**没有**调用 $h ⇒ 该 harness 不在门里"
      fi
    done
  fi
fi

# ══ 判据 ⑤：三个 harness 脚本本身存在 ═══════════════════════════════
for h in $IR_HARNESSES; do
  if [[ -f "$ROOT/scripts/audit/$h" ]]; then
    ok "harness 脚本存在：scripts/audit/$h"
  else
    bad "harness 脚本缺失：scripts/audit/$h"
  fi
done

# ══ 变异臂：证明「非注释行判定」真的有牙（否则本门可能恒绿）════════════
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/.workflow"
cat > "$fixture/.workflow/ci.yml" <<'YAML'
steps:
  - step: shell
    name: audit-guards
    script: |
      export PATH=$PATH:/usr/local/go/bin
      make guards-fast
YAML
cat > "$fixture/Makefile" <<'MK'
guards-fast: guards-ir-conversion
guards: guards-ir-conversion
guards-ir-conversion:
	@bash scripts/audit/ir-client-error-fix-mutation.sh
	@bash scripts/audit/ir-gemini-only-loss-mutation.sh
	@bash scripts/audit/ir-coerce-numeric-mutation.sh
MK

# 正控：夹具必须被判绿（否则下面的负控没有意义 ⇒ 量具自检）
if carrier_calls_guards_fast "$fixture/.workflow"; then
  ok "变异臂正控：非注释行的 make guards-fast 被检出"
else
  bad "变异臂正控失效：非注释行都没检出 ⇒ 判据本身坏了"
fi

# 负控：把该行挪进注释 ⇒ 必须判红
cat > "$fixture/.workflow/ci.yml" <<'YAML'
steps:
  - step: shell
    name: audit-guards
    script: |
      export PATH=$PATH:/usr/local/go/bin
      # make guards-fast
YAML
if carrier_calls_guards_fast "$fixture/.workflow"; then
  bad "变异臂负控失效：注释行里的 make guards-fast 仍被检出 ⇒ 判据无牙"
else
  ok "变异臂负控：注释行里的 make guards-fast 被正确排除"
fi

printf 'PASS=%d FAIL=%d\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
