#!/usr/bin/env bash
# tests/deploy_lib_candidate_chain_single_source_test.sh
# —— 【★ 共享部署库 SSOT 候选链的【★ 单一真相 ★】门 ★】（审计档 F51）
#
# ★ 这个门为什么存在（2026-11-09 · F51 的实测来由）：
#   仓内曾有**三份互不相同**的候选链（① scripts/_shared-lib.sh /
#   ② scripts/deploy-lib-resolve.sh / ③ tests/deploy_switch_lock_*_test.sh）：
#     ① 缺「仓内软链」；② 缺「新布局」；③ 是两者的并集（但同样缺「新布局」）。
#   ⇒ 审计档原文的后果：「★ 改代码不改测试 ⇒ 那个测试就在【测另一条链】、
#     而它照样绿 ⇒ ★ 「门是绿的」不再意味着「候选链一致」★」。
#
#   ★ 而【★ ② 缺的那一条恰恰是今天最要紧的那一条 ★】：
#     「新布局」＝ $HOME/workspace/ai-native-tools/deploy/deploy-lib
#     ⇒ 而 `~/workspace/ai-native-tools/deploy-lib`（兼容软链）是【★ 机器本地 ★】
#       的、**不在任何仓里** ⇒ ⇒ 在**没有**那根软链的机器上（CI runner /
#       干净工作区 / 重组后的新机）：
#         · ① 能解析到新布局 ✔
#         · ② **解析不到** ⇒ exit 64 ⇒ ★ deploy-154 / 245 / 211 第一步就退出 ★
#         · ③ 同样解析不到 ⇒ 对拍门整段 **SKIP** ⇒ ★ **假绿** ★
#   ⇒ ⇒ 本门把「链只有一条」变成**可检查的事实**。
#
# ★ 门的三组判据：
#   A 【★ 结构性 ★】：候选链**只在** scripts/deploy-lib-candidates.sh 里定义一次；
#     ★ 消费方**不得**自带链（★ 它们必须引用那一个文件 ★）。
#   B 【★ 行为性 ★】：两条解析路径（① / ②）对**同一条链**给出**同一个结论**；
#     ★ 且失败语义统一（rc=64 + 同一段可执行提示 + 确定性中止 ★）。
#   C 【★ 消费方行为 ★】：测试门与运维脚本在**只有新布局**的机器形态下
#     ★ **不得**再「找不到 SSOT」⇒ ★ 把「假绿」这条路堵死 ★。
#
# ★ 本门【★ 不写死任何布局名 ★】：夹具与判据面**全部由实现自己的候选链推导**
#   ⇒ ★ 布局改名 / 增删时本门跟着走、★ 不会假红 ★
#   ⇒ ★ 而「链少了一条」这种漂移 ⇒ **正是本门要抓的 ★（见 A2 / B1 ★）。
#
# ★ 零外部依赖、零仓库副作用（全部在 mktemp 里）⇒ 可进 CI 快门。
# Run: bash tests/deploy_lib_candidate_chain_single_source_test.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANON="$REPO_ROOT/scripts/deploy-lib-candidates.sh"
LIB_A="$REPO_ROOT/scripts/_shared-lib.sh"           # 解析路径 ①
LIB_B="$REPO_ROOT/scripts/deploy-lib-resolve.sh"    # 解析路径 ②

# ★ 消费方清单：★ 凡是【★ 会去定位 SSOT ★】的脚本 / 门 ★
CONSUMERS=(
  "scripts/_shared-lib.sh"
  "scripts/deploy-lib-resolve.sh"
  "scripts/deploy-observe-magpie-p0.sh"
  "tests/deploy_switch_lock_contract_test.sh"
  "tests/deploy_switch_lock_parity_test.sh"
  "tests/migration_retired_marker_contract_test.sh"
)

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); printf 'OK   %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1" >&2; }

TMP="$(mktemp -d)"
TMP="$(cd "$TMP" && pwd -P)"
trap 'rm -rf "$TMP"' EXIT

# ── 隔离的实现目录（★ 仓内软链候选必须落在这里、★ 不能是真实检出 ★）──────
IMPL="$TMP/impl"
mkdir -p "$IMPL"
cp "$LIB_A" "$IMPL/_shared-lib.sh"
cp "$LIB_B" "$IMPL/deploy-lib-resolve.sh"
cp "$CANON" "$IMPL/deploy-lib-candidates.sh"

# ── 候选链（★ 由实现自己给出 ★）────────────────────────────────────────────
chain_for() { # <HOME>
  HOME="$1" bash -c '
    set +e
    source "$1"
    deploy_lib_candidates
  ' _ "$IMPL/deploy-lib-candidates.sh" 2>/dev/null | sed 's/|[^|]*$//'
}

NEUTRAL="$TMP/home-neutral"
mkdir -p "$NEUTRAL"
CHAIN="$(chain_for "$NEUTRAL")"
NCHAIN="$(printf '%s\n' "$CHAIN" | awk 'NF{c++} END{print c+0}')"

# ── 恒绿护栏：★ 链为空 ⇒ 后面所有由链推导的断言都会**一次都不执行** ★ ──────
if [[ "$NCHAIN" -ge 2 ]]; then
  ok "D1 the shared chain exists and has >= 2 entries ($NCHAIN)"
else
  bad "D1 the shared chain must have >= 2 entries, got $NCHAIN (every derived assertion would be vacuous)"
fi
if [[ "${#CONSUMERS[@]}" -ge 4 ]]; then
  ok "D2 the consumer list is non-trivial (${#CONSUMERS[@]} consumers)"
else
  bad "D2 the consumer list must be non-trivial (got ${#CONSUMERS[@]})"
fi

# ★ D3 ★★ **HOME 级候选至少两条** ★ —— 机器上的布局可能有好几个（当前布局 /
#   兼容软链 / 重组前布局）⇒ ★ 只留一条 ⇒ ★ 「换个机器就不认得」的病复发 ★
#   ⇒ 而【★ 本条不写死布局名 ★】⇒ ★ 布局改名 / 增删时门跟着走 ★
_home_n=0
while IFS= read -r _c; do
  [[ -n "$_c" ]] || continue
  case "$_c" in
    "$NEUTRAL"/*) _home_n=$((_home_n + 1)) ;;
  esac
done <<< "$CHAIN"
if [[ "$_home_n" -ge 2 ]]; then
  ok "D3 the chain keeps >= 2 machine-level (HOME) candidates ($_home_n)"
else
  bad "D3 the chain must keep >= 2 HOME-level candidates, got $_home_n"
fi

# ★★ D4 ★★ **「软链背后的真实位置」必须也在链里 ★★ ——
#   ★ 为什么需要这条：★ 机器侧那根【★ 兼容软链 ★】（~/…/deploy-lib）是
#   ★ **机器本地**的、★ 不在任何仓里 ⇒ ★ 而它指向的**真实目录**才是 SSOT 的
#   ★ 规范位 ⇒ ★ 若链里只有那根软链、★ 没有它的目标 ⇒ ★ **一旦那根软链不存在
#   ★   （CI runner / 干净工作区）⇒ ★ 全链落空 ★** ⇒ ★ 那正是 F51 的现场形态 ★
#   ★ 口径（★ 不写死任何布局名 ★）：★ 取链上【★ 软链形态 ★】的候选、
#   ★ 解析它的真实目标 ⇒ ★ 那个目标必须【★ 也作为一条候选在链里 ★】
#   ★ ★ 关键：★ 比较的是【★ 路径字符串 ★】而不是【★ 解析后的同一目录 ★】——
#   ★   否则「★ 那根软链解析后与真实位同一个目录 ★」会把它自己判成满足
#   ★   ⇒ ★ 而那正是要防的形态（★ 链只认别名 ★）★★
#   ★ ★ 判据面用【★ 本机真实的 HOME ★】（★ 兼容软链是机器侧事实 ★）★
REAL_CHAIN="$(chain_for "$HOME")"
_sym_target=""
while IFS= read -r _c; do
  [[ -n "$_c" ]] || continue
  [[ -L "$_c" ]] || continue
  _t="$(cd "$_c" 2>/dev/null && pwd -P)" || continue
  [[ -n "$_t" ]] || continue
  _sym_target="$_t"
  break
done <<< "$REAL_CHAIN"
if [[ -z "$_sym_target" ]]; then
  ok "D4 no symlinked candidate on this machine (the invariant does not apply here)"
else
  _found=0
  while IFS= read -r _c; do
    [[ -n "$_c" ]] || continue
    # ★ 形态一：候选**本身就是**那个真实路径（★ 路径字符串相等 ★）
    if [[ "$_c" == "$_sym_target" ]]; then _found=1; break; fi
    # ★ 形态二：候选是【★ 非软链 ★】的目录且解析后就是那个真实路径
    [[ -L "$_c" ]] && continue
    _r="$(cd "$_c" 2>/dev/null && pwd -P)" || continue
    if [[ "$_r" == "$_sym_target" ]]; then _found=1; break; fi
  done <<< "$REAL_CHAIN"
  if [[ "$_found" -eq 1 ]]; then
    ok "D4 the real location behind the symlinked candidate is itself on the chain"
  else
    bad "D4 the real location behind the symlinked candidate ($_sym_target) is NOT on the chain"
    printf '  (★ a chain that only knows the machine-local alias breaks the moment that alias is absent ★)\n' >&2
  fi
fi

# ═════════════════════════════════════════════════════════════════════════
# A 【★ 结构性：链只有一处定义 ★】
# ═════════════════════════════════════════════════════════════════════════

# A1 唯一真相文件本身必须存在并定义链。
if [[ -f "$CANON" ]] && grep -q '^deploy_lib_candidates() {' "$CANON"; then
  ok "A1 the single source of the chain exists (scripts/deploy-lib-candidates.sh)"
else
  bad "A1 missing scripts/deploy-lib-candidates.sh (or it does not define deploy_lib_candidates())"
fi

# A2 ★★ 链的**布局形状**（★ 由链自己推导、★ 不写死 ★）在**全仓 shell 脚本**里
#    只允许出现在唯一真相文件里 ⇒ ★ 消费方一旦自带一份 ⇒ 当场红 ★
#
#    ★ 形状取法：把候选路径里的 $HOME 前缀换成占位符 ⇒ 剩下的相对形状
#      （如 workspace/ai-native-tools/deploy/deploy-lib）★ 就是可 grep 的指纹 ★
SHAPE_RE=""
_shapes=""
while IFS= read -r _c; do
  [[ -n "$_c" ]] || continue
  _shape="${_c/#$HOME/HOME}"
  _shape="${_shape#/private}"
  case "$_shape" in
    HOME/*) _shape="${_shape#HOME/}" ;;
    /*) continue ;;   # ★ 仓内软链候选（绝对路径）★ 不参与形状指纹 ★
  esac
  [[ -n "$_shape" ]] || continue
  _shapes="$_shapes$_shape
"
done <<< "$CHAIN"
SHAPE_RE="$(printf '%s' "$_shapes" | sort -u | tr '\n' '|')"
SHAPE_RE="${SHAPE_RE%|}"

leaked=""
if [[ -n "$SHAPE_RE" ]]; then
  while IFS= read -r _f; do
    [[ -n "$_f" ]] || continue
    case "$_f" in
      "$CANON") continue ;;
      *.go|*.json|*.md|*.ts|*.vue|*.yml|*.yaml) continue ;;
    esac
    _hits="$(grep -lE "$SHAPE_RE" "$_f" 2>/dev/null || true)"
    [[ -n "$_hits" ]] && leaked="$leaked$_hits
"
  done < <(find "$REPO_ROOT" -name '*.sh' -type f -not -path "*/node_modules/*" -not -path "*/.git/*" | sort)
fi
if [[ -z "$leaked" ]]; then
  ok "A2 the layout shapes exist in exactly one file (the single source)"
else
  bad "A2 the candidate chain was copied outside the single source:"
  printf '%s\n' "$leaked" >&2
  printf '  (★ a second copy means the consumers can drift again ★)\n' >&2
fi

# A3 每个消费方**必须引用**那一个真相文件（★ 不许自己另起一份 ★）
_missing_ref=""
for _c in "${CONSUMERS[@]}"; do
  [[ -f "$REPO_ROOT/$_c" ]] || { _missing_ref="$_missing_ref$_c (file missing)
"; continue; }
  if ! grep -qE '^[^#]*deploy-lib-candidates\.sh' "$REPO_ROOT/$_c"; then
    _missing_ref="$_missing_ref$_c (does not source the single source)
"
  fi
done
if [[ -z "$_missing_ref" ]]; then
  ok "A3 every consumer sources the single source of the chain"
else
  bad "A3 consumers must source scripts/deploy-lib-candidates.sh:"
  printf '%s' "$_missing_ref" >&2
fi

# A4 ★ 链的函数**不得**在别处再定义一份（★ 防「复制函数体」这一形态 ★）
_dup_defs="$(grep -rlE '^deploy_lib_candidates\(\) \{' "$REPO_ROOT" --include='*.sh' \
  --exclude-dir=node_modules --exclude-dir=.git 2>/dev/null | grep -v "^$CANON$" || true)"
if [[ -z "$_dup_defs" ]]; then
  ok "A4 deploy_lib_candidates() is defined in exactly one file"
else
  bad "A4 deploy_lib_candidates() is defined more than once:"
  printf '%s\n' "$_dup_defs" >&2
fi

# ═════════════════════════════════════════════════════════════════════════
# B 【★ 行为性：两个兄弟解析器对同一条链给出同一个结论 ★】
# ═════════════════════════════════════════════════════════════════════════

# 造一个「可用的 SSOT 目录」：三个判据文件都在 ⇒ 两个解析器都认它。
mk_ssot() {
  mkdir -p "$1"
  : > "$1/deploy-prereqs.sh"
  : > "$1/deploy-image-resolution.sh"
  : > "$1/parse-wrapper-flags.sh"
  : > "$1/lock.sh"
}

resolve_with() { # <impl 脚本> <HOME> [AIAN_DEPLOY_LIB]
  local entry="$1" home="$2" explicit="${3:-}"
  local out rc=0
  if [[ -n "$explicit" ]]; then
    out="$(HOME="$home" AIAN_DEPLOY_LIB="$explicit" bash -c \
      'set +e; source "$1"; printf "%s" "${AIAN_DEPLOY_LIB:-}"' _ "$entry" 2>"$TMP/err")" || rc=$?
  else
    out="$(HOME="$home" env -u AIAN_DEPLOY_LIB bash -c \
      'set +e; source "$1"; printf "%s" "${AIAN_DEPLOY_LIB:-}"' _ "$entry" 2>"$TMP/err")" || rc=$?
  fi
  printf '%s' "$out"
  return $rc
}

# ★ 清掉所有候选夹具 ⇒ ★ 「★ 一个候选都没有 ★」这类场景才是真的 ★
#   ★ 不清 ⇒ 上一轮留下的仓内软链夹具会被解析器**抢先命中** ⇒ ★ 假绿 ★
clear_candidate_fixtures() {
  rm -rf "$IMPL/deploy-lib"
  local _d
  for _d in "$TMP"/m-home-* "$TMP"/c-home; do
    [[ -d "$_d" ]] || continue
    local _cc
    while IFS= read -r _cc; do
      [[ -n "$_cc" ]] || continue
      case "$_cc" in
        "$TMP"/*) rm -rf "$_cc" ;;
      esac
    done < <(chain_for "$_d")
  done
}

# B1 ★★ 逐候选矩阵：★ 链上的**每一条**候选，在**只有它可用**时，
#    ★ 两个解析器都**必须**解析到它 ⇒ ★ 「链少一条」当场红 ★
#    ★ 这正是 F51 的本体缺陷（② 缺「新布局」⇒ ② 解析不到 ⇒ ② 与 ① 结论相反 ★）
matrix_fail=0
matrix_n=0
if [[ "$NCHAIN" -ge 2 ]]; then
  idx=0
  while IFS= read -r _c; do
    [[ -n "$_c" ]] || continue
    idx=$((idx + 1))
    matrix_n=$((matrix_n + 1))
    _home="$TMP/m-home-$idx"
    mkdir -p "$_home"
    _all="$(chain_for "$_home")"
    j=0
    while IFS= read -r _cc; do
      [[ -n "$_cc" ]] || continue
      j=$((j + 1))
      mkdir -p "$_cc"
      if [[ "$j" -eq "$idx" ]]; then
        mk_ssot "$_cc"     # ★ 只有这一条可用 ★
      else
        rm -rf "$_cc"       # ★ 其余的**不存在** ⇒ 「只有它可用」才成立 ★
      fi
    done <<< "$_all"
    _want="$(printf '%s\n' "$_all" | sed -n "${idx}p")"
    _got_a="$(resolve_with "$IMPL/_shared-lib.sh" "$_home")"
    _got_b="$(resolve_with "$IMPL/deploy-lib-resolve.sh" "$_home")"
    if [[ "$_got_a" != "$_want" ]]; then
      bad "B1 ① resolver did not pick candidate #$idx (want $_want got $_got_a)"
      matrix_fail=1
    fi
    if [[ "$_got_b" != "$_want" ]]; then
      bad "B1 ② resolver did not pick candidate #$idx (want $_want got $_got_b)"
      matrix_fail=1
    fi
  done <<< "$CHAIN"
  if [[ "$matrix_fail" -eq 0 ]]; then
    ok "B1 both resolvers pick every candidate of the shared chain (matrix $matrix_n x 2)"
  fi
else
  bad "B1 per-candidate matrix is vacuous (chain has $NCHAIN entries)"
fi

# B2 顺序语义：★ 全部候选都在 ⇒ 两个解析器都取**第一条** ★
_home="$TMP/m-home-all"; mkdir -p "$_home"
_all="$(chain_for "$_home")"
while IFS= read -r _cc; do [[ -n "$_cc" ]] && mk_ssot "$_cc"; done <<< "$_all"
_want="$(printf '%s\n' "$_all" | sed -n 1p)"
_got_a="$(resolve_with "$IMPL/_shared-lib.sh" "$_home")"
_got_b="$(resolve_with "$IMPL/deploy-lib-resolve.sh" "$_home")"
if [[ "$_got_a" == "$_want" && "$_got_b" == "$_want" ]]; then
  ok "B2 both resolvers honor the chain order (first candidate wins)"
else
  bad "B2 chain order not honored (want $_want / ①=$_got_a / ②=$_got_b)"
fi

# B3 显式值优先（★ 两个解析器都得让显式值说了算 ★）
_expl="$TMP/m-explicit"; mk_ssot "$_expl"
_got_a="$(resolve_with "$IMPL/_shared-lib.sh" "$_home" "$_expl")"
_got_b="$(resolve_with "$IMPL/deploy-lib-resolve.sh" "$_home" "$_expl")"
if [[ "$_got_a" == "$_expl" && "$_got_b" == "$_expl" ]]; then
  ok "B3 an explicit AIAN_DEPLOY_LIB wins in both resolvers"
else
  bad "B3 explicit AIAN_DEPLOY_LIB not honored (①=$_got_a / ②=$_got_b)"
fi

# B4 ★★ 失败语义统一：★ 显式值无效 ⇒ 两个解析器都 rc=64 + 同一段提示
#    + ★ 确定性中止（★ 调用方不继续跑 ★）★
clear_candidate_fixtures
_empty="$TMP/m-empty"; mkdir -p "$_empty"
for _pair in "①:$IMPL/_shared-lib.sh" "②:$IMPL/deploy-lib-resolve.sh"; do
  _tag="${_pair%%:*}"; _entry="${_pair#*:}"
  _rc=0
  _out="$(HOME="$_empty" AIAN_DEPLOY_LIB="$TMP/m-no-such-ssot" bash -c \
    'source "$1"; echo CALLER-CONTINUED' _ "$_entry" 2>"$TMP/err-bad")" || _rc=$?
  _err="$(cat "$TMP/err-bad")"
  if [[ "$_rc" -ne 64 ]]; then
    bad "B4 $_tag invalid explicit value -> rc 64 (got $_rc)"
  elif [[ "$_err" != *"missing deploy-lib SSOT"* ]]; then
    bad "B4 $_tag invalid explicit value -> names the missing SSOT"
  elif [[ "$_err" != *"fix: export AIAN_DEPLOY_LIB="* ]]; then
    bad "B4 $_tag invalid explicit value -> actionable fix hint"
  elif [[ "$_out" == *"CALLER-CONTINUED"* ]]; then
    bad "B4 $_tag invalid explicit value -> the caller kept running (not a deterministic abort)"
  else
    ok "B4 $_tag invalid explicit value -> rc 64 + actionable hint + deterministic abort"
  fi
done

# B5 ★★ 什么都找不到 ⇒ 两个解析器都 rc=64 + **同一段**文案 + 文案 == 链
for _pair in "①:$IMPL/_shared-lib.sh" "②:$IMPL/deploy-lib-resolve.sh"; do
  _tag="${_pair%%:*}"; _entry="${_pair#*:}"
  _rc=0
  _out="$(HOME="$_empty" env -u AIAN_DEPLOY_LIB bash -c \
    'source "$1"; echo CALLER-CONTINUED' _ "$_entry" 2>"$TMP/err-none")" || _rc=$?
  _err="$(cat "$TMP/err-none")"
  if [[ "$_rc" -ne 64 ]]; then
    bad "B5 $_tag no SSOT anywhere -> rc 64 (got $_rc)"
  elif [[ "$_err" != *"missing AIAN_DEPLOY_LIB"* ]]; then
    bad "B5 $_tag no SSOT anywhere -> actionable hint"
  elif [[ "$_out" == *"CALLER-CONTINUED"* ]]; then
    bad "B5 $_tag no SSOT anywhere -> the caller kept running"
  else
    ok "B5 $_tag no SSOT anywhere -> rc 64 + actionable hint + deterministic abort"
  fi
done
# ★ 文案里列出的布局 == 链（★ 两个解析器都要 ★）
for _pair in "①:$IMPL/_shared-lib.sh" "②:$IMPL/deploy-lib-resolve.sh"; do
  _tag="${_pair%%:*}"; _entry="${_pair#*:}"
  HOME="$_empty" env -u AIAN_DEPLOY_LIB bash -c 'source "$1"' _ "$_entry" >/dev/null 2>"$TMP/err-list"
  _listed="$(grep -E '^[[:space:]]+(~|/)' "$TMP/err-list" | awk '{print $1}' \
    | sed "s|^~|$_empty|" | LC_ALL=C sort)"
  _expected="$(chain_for "$_empty" | LC_ALL=C sort)"
  if [[ "$_listed" == "$_expected" ]]; then
    ok "B5 $_tag the error text lists exactly the chain (no drift)"
  else
    bad "B5 $_tag the error text drifted from the chain"
    printf '     listed:   %s\n' "$(printf '%s' "$_listed" | tr '\n' ' ')" >&2
    printf '     expected: %s\n' "$(printf '%s' "$_expected" | tr '\n' ' ')" >&2
  fi
done

# ═════════════════════════════════════════════════════════════════════════
# C 【★ 消费方行为：★ 只有新布局的机器形态下 ★ 门不再「找不到 SSOT」 ★】
# ═════════════════════════════════════════════════════════════════════════
#
# ★ 这一组是 F51 的**本体断言**：★ 旧链缺「新布局」⇒ ★ 测试门会整段 SKIP
#   ⇒ ★ 「门绿了」而事实没被比过 ⇒ ★ **假绿** ★
#
# ★ 夹具：★ HOME 下只有「新布局」那一条候选（★ 由链自己推导 ★）
_home="$TMP/c-home"; mkdir -p "$_home"
_c_layout="$(chain_for "$_home" | sed -n 2p)"   # ★ 链的第 2 条 = HOME 下的当前布局 ★
if [[ -n "$_c_layout" ]]; then
  clear_candidate_fixtures
  mk_ssot "$_c_layout"
  _found="$(HOME="$_home" bash -c '
    set +e
    source "$1"
    deploy_lib_find lock.sh
  ' _ "$IMPL/deploy-lib-candidates.sh" 2>/dev/null)"
  if [[ "$_found" == "$_c_layout" ]]; then
    ok "C1 the shared lookup finds the SSOT at the current layout (the layout-only machine shape)"
  else
    bad "C1 the shared lookup did not find the SSOT at the current layout (want $_c_layout got $_found)"
  fi

  # ★★ C2：对拍门在同样形态下【★ 不得】再整段 SKIP（★ 那就是「假绿」★）
  #   ★ ★ 必须在【★ 隔离的仓副本 ★】里跑：★ 真实检出里的 `scripts/deploy-lib`
  #   ★   是一根**能解析**的相对软链 ⇒ ★ 它会**抢先命中** ⇒ ★ 那样测的就不是
  #   ★   「只有新布局」的机器形态了（★ 而那正是 F51 的现场形态 ★）★
  _repo2="$TMP/repo2"
  mkdir -p "$_repo2/scripts" "$_repo2/tests/lib"
  cp "$REPO_ROOT/scripts/deploy-local.sh" "$REPO_ROOT/scripts/deploy-local-lib.sh" \
     "$REPO_ROOT/scripts/deploy-lib-candidates.sh" "$_repo2/scripts/" 2>/dev/null
  cp "$REPO_ROOT/tests/deploy_switch_lock_parity_test.sh" \
     "$REPO_ROOT/tests/deploy_lock_parity_baseline.json" "$_repo2/tests/" 2>/dev/null
  [[ -f "$REPO_ROOT/tests/lib/fake-flock.sh" ]] && cp "$REPO_ROOT/tests/lib/fake-flock.sh" "$_repo2/tests/lib/"
  _par_out="$(HOME="$_home" bash "$_repo2/tests/deploy_switch_lock_parity_test.sh" 2>&1 || true)"
  if printf '%s' "$_par_out" | grep -q 'SKIP: shared deploy lib (lock.sh) not found'; then
    bad "C2 the cross-repo parity gate still skips when the SSOT exists at the current layout (★ 假绿 ★)"
    printf '%s\n' "$(printf '%s' "$_par_out" | head -5)" >&2
  elif printf '%s' "$_par_out" | grep -q "SSOT: $_c_layout"; then
    ok "C2 the cross-repo parity gate compared against the SSOT at the current layout (no 假绿)"
  else
    bad "C2 the cross-repo parity gate did not report the SSOT it used"
    printf '%s\n' "$(printf '%s' "$_par_out" | head -8)" >&2
  fi
else
  bad "C1 the shared chain has no HOME-based candidate to build the layout-only machine shape from"
fi

printf 'deploy_lib_candidate_chain_single_source: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
