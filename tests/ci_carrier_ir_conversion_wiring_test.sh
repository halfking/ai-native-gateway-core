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
#   ④ `guards-ir-conversion` 的配方里逐个调用了清单里的每个变异 harness
#   ④b ★ **反向对拍**：★ 配方里出现的**每一个** harness 都必须**在清单里**
#       ⇒ ⇒ 「新增了 harness 却没登记」当场红（★ 根治清单漂移）
#   ⑤ 清单里的 harness 脚本本身存在
#   ⑥ ★ **磁盘对拍**：★ 磁盘上每个 `scripts/audit/ir-*-mutation.sh`
#       都必须在清单里（★ glob 自维护 ⇒ 新增 shell harness 自动被覆盖）
#
# ★ 为什么要加 ④b 与 ⑥（★ 本轮实测踩到的坑）★★：
#   ★ 本轮往 `guards-ir-conversion` 里**新增**了第 4 个 harness（Python 版的
#   ★ 通用变异验证器按组调用）⇒ ★★ 若只有判据 ④（清单 → 配方）⇒ ★★
#   ★★ **新 harness 不在清单里也照样绿** ⇒ ★★ 清单会静默漂移 ⇒ ★★
#   ★★ 这正是本仓 LESSONS「手写清单必然漂移」的同款 ⇒ ★★ 必须**双向对拍** ★★。
#
# ★ 变异臂：★ 三条合成夹具分别证明「非注释行判定」「清单→配方」「配方→清单」
#   真的有牙 ⇒ ★ 否则本门可能恒绿（量具自检）。
#
# 纪律：bash 3.2 可跑（macOS 自带 bash）★ 零外部依赖 ★ 读不到判据 ⇒ fail-closed。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAKEFILE="$ROOT/Makefile"
WORKFLOW_DIR="$ROOT/.workflow"

# IR 门在载体链路上的入口 target。
IR_TARGET="guards-ir-conversion"

# ★★★ 清单条目格式：`<脚本文件名>[ <额外参数>...]` ★★★
#   · 判据 ④ 用**整条**条目做子串匹配（★ 连参数一起钉 ⇒ 参数被改也会红）
#   · 判据 ⑤ 用**首词**（脚本名）做存在性检查
IR_HARNESSES=(
  "ir-client-error-fix-mutation.sh"
  "ir-gemini-only-loss-mutation.sh"
  "ir-coerce-numeric-mutation.sh"
  "mutation-verify-2026-11-11.py --group toolcall-canonical"
)

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

# ── 清单条目的脚本名（首词）────────────────────────────────────────────
harness_script_name() { printf '%s' "$1" | awk '{print $1}'; }

# ── 配方里出现的所有 harness 脚本名（去重、排序）────────────────────────
# ★ 只认 `scripts/audit/<名字>` 形态 ⇒ `go test ./internal/ir/...` 这类不会进来。
recipe_harness_files() {
  printf '%s\n' "$1" \
    | grep -oE 'scripts/audit/[A-Za-z0-9._-]+' \
    | sed 's#^scripts/audit/##' \
    | sort -u
}

# ── 清单里的所有 harness 脚本名（去重、排序）────────────────────────────
listed_harness_files() {
  local e
  for e in "${IR_HARNESSES[@]}"; do
    harness_script_name "$e"
  done | sort -u
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

  # ══ 判据 ④：配方里逐个调用清单里的每个 harness ═════════════════════
  recipe="$(target_recipe "$IR_TARGET" "$MAKEFILE")"
  if [[ -z "${recipe// /}" ]]; then
    bad "Makefile 里找不到 $IR_TARGET 的配方 ⇒ 判据失效（fail-closed）"
  else
    for entry in "${IR_HARNESSES[@]}"; do
      if printf '%s\n' "$recipe" | grep -qF "$entry"; then
        ok "$IR_TARGET 配方调用了 $entry"
      else
        bad "$IR_TARGET 配方**没有**调用 $entry ⇒ 该 harness 不在门里"
      fi
    done

    # ══ 判据 ④b：反向对拍 —— 配方里的每个 harness 都必须在清单里 ═══════
    # ★ 为什么必须有这一条：★ 判据 ④ 只查「清单 → 配方」⇒ ⇒ **新增一个
    # ★ harness 却忘了登记 ⇒ 照样绿** ⇒ ⇒ 清单静默漂移 ⇒ ⇒ 必须双向。
    listed="$(listed_harness_files)"
    unregistered=""
    while IFS= read -r f; do
      [[ -z "$f" ]] && continue
      if ! printf '%s\n' "$listed" | grep -qxF "$f"; then
        unregistered="$unregistered $f"
      fi
    done <<EOF
$(recipe_harness_files "$recipe")
EOF
    if [[ -z "${unregistered// /}" ]]; then
      ok "配方里出现的每个 harness 都在清单里（反向对拍通过）"
    else
      bad "配方里出现了**未登记**的 harness：$unregistered ⇒ ⇒ 清单漂移了 ⇒ ⇒ "\
"要么把它登记进清单，要么把它从配方里拿掉"
    fi
  fi
fi

# ══ 判据 ⑤：清单里的 harness 脚本本身存在 ═══════════════════════════
for entry in "${IR_HARNESSES[@]}"; do
  script="$(harness_script_name "$entry")"
  if [[ -f "$ROOT/scripts/audit/$script" ]]; then
    ok "harness 脚本存在：scripts/audit/$script"
  else
    bad "harness 脚本缺失：scripts/audit/$script"
  fi
done

# ══ 判据 ⑥：磁盘上每个 ir-*-mutation.sh 都必须在清单里 ═══════════════
# ★ glob 自维护 ⇒ ⇒ 新增一个 IR shell harness ⇒ 它自动进入覆盖面 ⇒
# ⇒ ⇒ 不需要**手工**往清单里加一行（★ 加了也可能忘 ⇒ 判据⑥会告诉你）。
listed_all="$(listed_harness_files)"
missing_from_list=""
for f in "$ROOT"/scripts/audit/ir-*-mutation.sh; do
  [[ -f "$f" ]] || continue
  base="$(basename "$f")"
  if ! printf '%s\n' "$listed_all" | grep -qxF "$base"; then
    missing_from_list="$missing_from_list $base"
  fi
done
if [[ -z "${missing_from_list// /}" ]]; then
  ok "磁盘上每个 ir-*-mutation.sh 都在清单里（glob 对拍通过）"
else
  bad "磁盘上的 IR harness 没进清单：$missing_from_list ⇒ ⇒ 登记进清单，否则它**不在门里**"
fi

# ══ 变异臂 ①：证明「非注释行判定」真的有牙（否则本门可能恒绿）════════
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

# ★ 夹具 Makefile 的配方**由清单生成** ⇒ ⇒ 清单与夹具永远同源。
make_fixture_makefile() {
  local out="$1"; shift
  {
    echo "guards-fast: guards-ir-conversion"
    echo "guards: guards-ir-conversion"
    echo "guards-ir-conversion:"
    local e
    for e in "${IR_HARNESSES[@]}"; do
      case "$e" in
        *.py) echo "	@python3 scripts/audit/$e" ;;
        *)    echo "	@bash scripts/audit/$e" ;;
      esac
    done
  } > "$out"
}

make_fixture_makefile "$fixture/Makefile"

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

# ══ 变异臂 ②：判据 ④（清单 → 配方）真的有牙 ══════════════════════════
# ★ 从夹具 Makefile 里删掉清单的**最后一条** ⇒ ⇒ 判据 ④ 必须判红。
fixture_recipe="$(target_recipe "$IR_TARGET" "$fixture/Makefile")"
last_entry="${IR_HARNESSES[$((${#IR_HARNESSES[@]} - 1))]}"
grep -vF "scripts/audit/$last_entry" "$fixture/Makefile" > "$fixture/Makefile.trimmed" \
  && mv "$fixture/Makefile.trimmed" "$fixture/Makefile"
trimmed_recipe="$(target_recipe "$IR_TARGET" "$fixture/Makefile")"
if printf '%s\n' "$trimmed_recipe" | grep -qF "$last_entry"; then
  bad "变异臂②失效：删掉清单条目后配方里仍有它 ⇒ 判据 ④ 无牙"
else
  ok "变异臂②：删掉清单条目后判据 ④ 能检出（无牙会假绿）"
fi
# 复原夹具
make_fixture_makefile "$fixture/Makefile"

# ══ 变异臂 ③：判据 ④b（配方 → 清单）真的有牙 ═════════════════════════
# ★ 往夹具配方里塞一个**未登记**的 harness ⇒ ⇒ 判据 ④b 必须判红。
cat >> "$fixture/Makefile" <<'MK'
	@bash scripts/audit/ir-not-registered-mutation.sh
MK
unreg_recipe="$(target_recipe "$IR_TARGET" "$fixture/Makefile")"
unreg_listed="$(listed_harness_files)"
unreg_hit=0
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  if ! printf '%s\n' "$unreg_listed" | grep -qxF "$f"; then
    unreg_hit=1
  fi
done <<EOF
$(recipe_harness_files "$unreg_recipe")
EOF
if [[ "$unreg_hit" -eq 1 ]]; then
  ok "变异臂③：未登记的 harness 被判据 ④b 抓住（清单漂移会当场红）"
else
  bad "变异臂③失效：未登记的 harness 没被抓住 ⇒ 判据 ④b 无牙 ⇒ 清单会静默漂移"
fi
# 复原夹具
make_fixture_makefile "$fixture/Makefile"

# ══ 变异臂 ④：判据 ⑥（磁盘 glob → 清单）真的有牙 ══════════════════════
# ★ 在夹具的 scripts/audit 下造一个**不在清单里**的 ir-*-mutation.sh
#   ⇒ ⇒ 判据 ⑥ 必须判红。
mkdir -p "$fixture/scripts/audit"
: > "$fixture/scripts/audit/ir-orphan-mutation.sh"
glob_listed="$(listed_harness_files)"
glob_missing=""
for f in "$fixture"/scripts/audit/ir-*-mutation.sh; do
  [[ -f "$f" ]] || continue
  base="$(basename "$f")"
  if ! printf '%s\n' "$glob_listed" | grep -qxF "$base"; then
    glob_missing="$glob_missing $base"
  fi
done
if [[ -n "${glob_missing// /}" ]]; then
  ok "变异臂④：磁盘上未登记的 IR harness 被判据 ⑥ 抓住（glob 对拍有牙）"
else
  bad "变异臂④失效：未登记的磁盘 harness 没被抓住 ⇒ 判据 ⑥ 无牙"
fi

printf 'PASS=%d FAIL=%d\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
