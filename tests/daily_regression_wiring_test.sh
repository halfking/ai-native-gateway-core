#!/usr/bin/env bash
# daily_regression_wiring_test.sh —— 每日回归机制接线门（2026-11-11 协议转换审计轮）
#
# ── 本门存在的根因 ──────────────────────────────────────────────────────
# ★ 「每日回归」这件事最容易的失败形态是：**仓里放一个脚本，没人调它** ⇒
#   ⇒ 按本仓纪律「门没有调用方 = 声明不是门」，那它不是机制，是文件。
# ★ 而「放一个脚本 + 在载体里加一行」还只是**接线**；真正的失效形态有两个：
#   ① **节奏断了**（推/PR 会跑，但没有每日触发）
#   ② **集合腐坏了**（包集合是手抄的 ⇒ 新增一个 import internal/ir 的包
#      不会被每日回归覆盖 ⇒ ⇒ 而门还是绿的 ⇒ ⇒ 覆盖缺口被静默掩盖）
# ⇒ 本门把这三条都做成**可执行判定**。
#
# ── 本门钉的不变量 ──────────────────────────────────────────────────────
#   ① 入口脚本存在
#   ② Makefile 有 `daily-regression` target 且**引用入口脚本**
#   ③ 载体（`.workflow/` 下每个流水线配置）里有**非注释行**调用它
#   ④ ★ **派生性**（行为级）：★ 用 PATH shim 把 `go list -f` 的输出换成空的
#      ⇒ ⇒ 入口脚本必须**当场 fail-closed**（rc≠0 且给出专属失败串）★★
#      ⇒ ★★ 这条同时钉住两件事：★「派生为空必须红」+ ★「集合不是手抄常量」
#      ⇒ ★★ 因为若有人把集合改成手抄常量，shim 就拦不住它 ⇒ ⇒ 判据④变红 ★★
#   ⑤ 变异臂：把载体里的调用挪进注释 ⇒ 必须判红（证明③有牙）
#
# 纪律：bash 3.2 可跑（macOS 自带 bash）★ 零外部依赖 ★ 读不到判据 ⇒ fail-closed。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAKEFILE="$ROOT/Makefile"
WORKFLOW_DIR="$ROOT/.workflow"
ENTRY_REL="scripts/audit/daily_regression.sh"
ENTRY="$ROOT/$ENTRY_REL"
MAKE_TARGET="daily-regression"

pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  ✅ %s\n' "$*"; }
bad() { fail=$((fail+1)); printf '  ❌ %s\n' "$*"; }

# ── 取 Makefile 某个 target 的配方（make 配方行以 TAB 开头）────────────
target_recipe() {
  awk -v t="$1" '
    $0 ~ "^" t "[ \t]*:" { inrec=1; next }
    inrec {
      if (substr($0,1,1) != "\t") exit
      print
    }' "$2"
}

# ── 载体里有没有**非注释行**调 `make <target>` ─────────────────────────
# ★ 注释行（`#` 开头，缩进后带 # 的也算）不算 ⇒ 「注释里写不算」。
carrier_calls_target() {
  local wfdir="$1" target="$2" f
  [[ -d "$wfdir" ]] || return 1
  for f in "$wfdir"/*.yml "$wfdir"/*.yaml; do
    [[ -f "$f" ]] || continue
    # ^[^#]* ⇒ 不允许跨过注释符；因此 `# ... make daily-regression` 不会被命中。
    if grep -qE "^[^#]*\bmake[[:space:]]+$target\b" "$f"; then
      return 0
    fi
  done
  return 1
}

# ══ 判据 ①：入口脚本存在 ══════════════════════════════════════════════
if [[ -f "$ENTRY" ]]; then
  ok "入口脚本存在：$ENTRY_REL"
else
  bad "入口脚本缺失：$ENTRY_REL ⇒ ⇒ 每日回归机制不存在"
fi

# ══ 判据 ②：Makefile target 存在且引用入口 ═══════════════════════════
if [[ ! -f "$MAKEFILE" ]]; then
  bad "Makefile 不存在 ⇒ 判据失效（fail-closed）"
else
  recipe="$(target_recipe "$MAKE_TARGET" "$MAKEFILE")"
  if [[ -z "${recipe// /}" ]]; then
    bad "Makefile 里找不到 $MAKE_TARGET 的配方 ⇒ 每日回归没有 Makefile 入口"
  else
    if printf '%s\n' "$recipe" | grep -qF "$ENTRY_REL"; then
      ok "Makefile 的 $MAKE_TARGET 引用了 $ENTRY_REL"
    else
      bad "Makefile 的 $MAKE_TARGET **没有**引用 $ENTRY_REL ⇒ ⇒ 入口与 target 脱节"
    fi
  fi
fi

# ══ 判据 ③：载体里有非注释行调 `make daily-regression` ═══════════════
if carrier_calls_target "$WORKFLOW_DIR" "$MAKE_TARGET"; then
  ok "在跑的载体里有**非注释行**调用 make $MAKE_TARGET"
else
  bad "在跑的载体里没有非注释行调用 make $MAKE_TARGET ⇒ ⇒ 每日回归在载体上是空转的"
fi

# ══ 判据 ④：派生性（行为级 —— PATH shim 把 go list -f 换成空）═════════
# ★ 为什么用行为级而不是字面匹配：★ 「集合是派生的」这件事没法靠 grep 判准
#   （写成常量也可能长得像）⇒ ⇒ 唯一能验的办法是**让派生源失效**⇒ ⇒
#   ★ 派生源失效后脚本必须红 ⇒ ⇒ 若它是手抄常量就不会红 ⇒ ⇒ 判据有牙 ★。
shim_dir="$(mktemp -d)"
trap 'rm -rf "$shim_dir"' EXIT
REAL_GO="$(command -v go || true)"
if [[ -z "$REAL_GO" ]]; then
  bad "找不到 go ⇒ ⇒ 判据④无法自证 ⇒ fail-closed"
else
  cat > "$shim_dir/go" <<SH
#!/usr/bin/env bash
# ★ 拦截「列包」那一类调用（带 -f）⇒ 输出空 ⇒ ⇒ 派生集合为空。
# ★ 其余（go list -m / go test / go tool …）**透传真实 go** ⇒ ⇒
# ★   这样脚本只会在「派生为空」这一处红 ⇒ ⇒ 归因精确。
for a in "\$@"; do
  if [[ "\$a" == "-f" ]]; then
    exit 0
  fi
done
exec "$REAL_GO" "\$@"
SH
  chmod +x "$shim_dir/go"
  out="$(PATH="$shim_dir:$PATH" bash "$ENTRY" 2>&1)"
  rc=$?
  if [[ "$rc" -eq 0 ]]; then
    bad "派生源被换成空之后，入口脚本**仍然 rc=0** ⇒ ⇒ 集合不是机器派生的（或派生为空没有 fail-closed）⇒ ⇒ 覆盖缺口会被静默掩盖"
  elif printf '%s\n' "$out" | grep -q "派生出的影响面包集合为空"; then
    ok "派生源为空时入口脚本 fail-closed（rc=${rc}，且给出专属失败串）⇒ 集合确为机器派生"
  else
    bad "派生源为空时入口脚本红了（rc=${rc}），但失败原因**不是**派生为空 ⇒ ⇒ 归因不准 ⇒ 输出尾部："
    printf '%s\n' "$out" | tail -12 | sed 's/^/      /'
  fi
fi

# ══ 判据 ⑤：变异臂 —— ③ 判定必须有牙 ════════════════════════════════
fixture="$(mktemp -d)"
mkdir -p "$fixture/.workflow"
cat > "$fixture/.workflow/ci.yml" <<'YAML'
steps:
  - step: shell
    name: daily-regression
    script: |
      export PATH=$PATH:/usr/local/go/bin
      make daily-regression
YAML
if carrier_calls_target "$fixture/.workflow" "$MAKE_TARGET"; then
  ok "变异臂正控：非注释行的 make $MAKE_TARGET 被检出"
else
  bad "变异臂正控失效：非注释行都没检出 ⇒ 判据本身坏了"
fi
cat > "$fixture/.workflow/ci.yml" <<'YAML'
steps:
  - step: shell
    name: daily-regression
    script: |
      export PATH=$PATH:/usr/local/go/bin
      # make daily-regression
YAML
if carrier_calls_target "$fixture/.workflow" "$MAKE_TARGET"; then
  bad "变异臂负控失效：注释行里的 make $MAKE_TARGET 仍被检出 ⇒ 判据无牙"
else
  ok "变异臂负控：注释行里的 make $MAKE_TARGET 被正确排除"
fi

printf 'PASS=%d FAIL=%d\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
