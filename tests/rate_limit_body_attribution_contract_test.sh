#!/usr/bin/env bash
# =====================================================================
# tests/rate_limit_body_attribution_contract_test.sh
#
# ★ 限流正文归因契约门（★ 2026-11-10 · 审计 F50）
#
# ★ 这道门为什么存在
#
# 审计登记项 F50 记的是：
#   「★ 上游用**请求侧 4xx** 回了一个**限流形态**的正文（★ 例：
#     HTTP 400 + {"error":{"message":"rate limit exceeded"}}）
#     ⇒ 而仓里**只认状态码 429** ⇒ 那类正文落到了 KindRequestRejected
#     ⇒ ★ **归因丢了**（★ 而这条丢失是**故意**的：★ §五.4.1 当年提的方向是
#     「新增正文规则 ⇒ KindRateLimit」⇒ 而那条方向**与事故教训相悖** ⇒
#     KindRateLimit 会把 (凭据,模型) 按 http_429 拉黑并参与连败累计 ⇒
#     把放大器的两条腿放回来 ⇒ ⇒ **方向已否决**）。
#
# ⇒ 本轮（★ 属主裁决「★ 新增限流正文规则」）落成的是**第三条路**：
#   **新增一个「不惩罚节点」的独立 kind**（KindRateLimitObserved）⇒
#   · 归因**恢复**（★ 不再笼统地叫「请求被拒」）
#   · 而**不**继承限流族的惩罚语义（★ 不拉黑、★ 不连败累计、★ 不扇出探针）
#   · 且**保留跨凭据故障转移**（★ 这一把钥匙撞了上游限流、★ 兄弟凭据可能没有）
#
# ★ ★ 门管八件事（★ 逐条机器可判）：
#   ① 这个 kind **存在**、且**不等于** KindRateLimit（★ 不能复活被否决的方向）
#   ② **请求侧 4xx ＋ 限流形态正文** ⇒ 落这个 kind
#   ③ **两个分类器逐字同判**（★ ClassifyErrorWithBody 与 ClassifyResponseBody
#      不许分叉 ⇒ 否则「同一段正文两个归因」）
#   ④ 正文正则**不含 \b**（★ BSD libc 的 regcomp 不认 \b ⇒ ★ 跨平台）
#   ⑤ 这个 kind **不惩罚节点**（★ 归责 PenaltyNone、★ 不扇出自动探针）
#   ⑥ 行为判据绿（★ errorsx 自己的限流正文归因用例）
#   ⑦ 4xx 矩阵门仍然绿（★ 两个分类器不许分叉）
#   ⑧ **变异臂**：★ 把正文规则打坏 ⇒ 行为判据必须转红（★ 证明门有牙齿）
#
# ★ 用法：
#   bash tests/rate_limit_body_attribution_contract_test.sh
#       ⇒ 判真实树（默认）⇒ 正确树上应当 PASS=… FAIL=0、退出码 0。
#   bash tests/rate_limit_body_attribution_contract_test.sh --subject mutant
#       ⇒ 判**变异体**（正文规则被打坏的那份）⇒ 行为判据必须红 ⇒ 退出码 ≠ 0。
#       ★ 这就是「门在变异下红」的可复现入口 ★。
#
# ★ 零仓库副作用：★ 变异体建在**仓库内**的临时包目录里（★ go test 需要它
#   在模块内才解析得到），EXIT trap 必删 ⇒ 正常跑完仓库是干净的。
# =====================================================================
set -uo

# 2026-11-11（48h 审计第二轮）：接入仓内专用 Go 构建缓存（r1047 约定，
# gocache_script_family_guard 门判据①——本脚本调 go test ⇒ 必须指向专用目录）。
export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" guard)" pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SUBJECT="real"                       # real | mutant
MUT_DIR_REL="errorsx_f50_mutation_probe"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --subject) SUBJECT="${2:-}"; shift 2 || exit 64 ;;
    -h|--help) sed -n '2,55p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) printf 'unknown arg: %s\n' "$1" >&2; exit 64 ;;
  esac
done

case "$SUBJECT" in
  real|mutant) ;;
  *) printf 'FATAL: --subject 只接受 real|mutant（收到 %q）\n' "$SUBJECT" >&2; exit 64 ;;
esac

MUT_DIR="$REPO_ROOT/$MUT_DIR_REL"

# ★ 零仓库副作用：★ 变异体建在仓库内（go test 需要它在模块内才解析得到）
#   ⇒ EXIT trap 必删 ⇒ 正常跑完仓库是干净的。
cleanup() {
  cd / 2>/dev/null || true
  rm -rf "$MUT_DIR"
}
trap cleanup EXIT

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

command -v go >/dev/null 2>&1 || { printf 'FATAL: PATH 上没有 go —— 本门跑不了\n' >&2; exit 64; }

CLASSIFY="errorsx/classify.go"

# ── 判定器：跑一组聚焦用例，回 0 = 绿、回非 0 = 红 ─────────────────────
# $1 = 包目录（相对仓库根），$2 = -run 正则，$3 = 标签
run_go_test() {
  local pkg="$1" run="$2" label="$3" out rc
  out="$(CGO_ENABLED=0 go test "$pkg" -run "$run" -count=1 2>&1)"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    printf -- '--- %s: go test %s -run %s ⇒ rc=%s ---\n' "$label" "$pkg" "$run" "$rc" >&2
    printf -- '%s\n' "$out" | tail -25 | sed 's/^/    /' >&2
  fi
  return $rc
}

# ── 判定器：跑一组聚焦用例，**并**要求至少真跑到 1 个用例 ──────────────
# ★ 为什么多这一层：★ `go test -run <不存在的名字>` 是**绿的** ⇒
#   ⇒ 一个写错的名字会让判据**空跑** ⇒ 门绿着漏 ⇒ ⇒ 每次跑都必须
#   **证明**判据面非空。
run_go_test_nonempty() {
  local pkg="$1" run="$2" label="$3" out rc n
  out="$(CGO_ENABLED=0 go test "$pkg" -run "$run" -count=1 -v 2>&1)"
  rc=$?
  n=$(printf '%s\n' "$out" | grep -c '^--- PASS' || true)
  if [[ $rc -ne 0 ]]; then
    printf -- '--- %s: go test %s -run %s ⇒ rc=%s ---\n' "$label" "$pkg" "$run" "$rc" >&2
    printf -- '%s\n' "$out" | tail -25 | sed 's/^/    /' >&2
    return 1
  fi
  if [[ "$n" -eq 0 ]]; then
    printf -- '--- %s: go test %s -run %s ⇒ 一个用例都没跑到（★ 判据面是空的 ⇒ 绿着漏）---\n' \
      "$label" "$pkg" "$run" >&2
    return 1
  fi
  return 0
}

# ═════════════════════════════════════════════════════════════════════
# 0. 造变异体：★ 把「限流正文规则」打坏的那份实现
# ═════════════════════════════════════════════════════════════════════
rm -rf "$MUT_DIR"
mkdir -p "$MUT_DIR" || { printf 'FATAL: 建不出变异体目录 %s\n' "$MUT_DIR" >&2; exit 1; }
shopt -s nullglob
for f in errorsx/*.go; do
  case "$f" in *_test.go) continue ;; esac
  cp "$f" "$MUT_DIR/" || { printf 'FATAL: 复制 %s 失败\n' "$f" >&2; exit 1; }
done
# ★ 只带上本门判据用的那一个用例文件（★ 不复制其它用例 ⇒ 变异体里只有 F50 判据）
cp errorsx/classify_rate_limit_observed_test.go "$MUT_DIR/" \
  || { printf 'FATAL: 复制 F50 用例文件失败\n' >&2; exit 1; }

MUT_SRC="$MUT_DIR/classify.go"
if [[ ! -f "$MUT_SRC" ]]; then
  bad "变异体里没有 classify.go —— 变异臂没得打"
else
  # ★ 变异：把整条正文规则换成一个**永不匹配**的形态。
  #   ★ 用 sed 的**块替换**（★ 从 `var rateLimitBodyRe = regexp.MustCompile(`
  #     那一行到顶格的 `)` 那一行）⇒ ★ 不依赖正则能配平嵌套括号
  #     （★ 这条正文的原式里有 `(second|minute|hour)` 这种**嵌套**括号
  #       ⇒ 任何「一层括号」的正则都配不平 ⇒ 上一次的变异臂就是这么哑掉的）。
  #   ★ 替换后**仍能编译** ⇒ 变异体的红必须是**行为红**、不是编译红。
  sed -e '/^var rateLimitBodyRe = regexp\.MustCompile(/,/^)$/c\
var rateLimitBodyRe = regexp.MustCompile("ZZZ_NEVER_MATCH_ZZZ")' \
    "$MUT_SRC" > "$MUT_SRC.tmp" && mv "$MUT_SRC.tmp" "$MUT_SRC"
  if grep -q 'ZZZ_NEVER_MATCH_ZZZ' "$MUT_SRC" && ! grep -q '^var rateLimitBodyRe = regexp\.MustCompile($' "$MUT_SRC"; then
    ok "0. 变异体已就位（限流正文规则换成永不匹配的形态）"
  else
    bad "0. 变异没打上（规则形态变了？）⇒ 变异臂是假的，本门必须红"
  fi
fi

# ═════════════════════════════════════════════════════════════════════
# 1. ★★ 变异臂自检：★ 正文规则被打坏 ⇒ 行为判据必须转红 ★★
# ═════════════════════════════════════════════════════════════════════
MUT_PKG="./$MUT_DIR_REL"
if run_go_test "$MUT_PKG" 'RateLimitObserved' "变异臂·限流正文归因判据"; then
  bad "① ★★ 变异臂：正文规则被打坏后判据仍然判绿 ⇒ 判据无牙齿（假绿）★★"
else
  ok "① 变异臂：正文规则被打坏 ⇒ 限流正文归因判据转红（判据有牙齿）"
fi
# ★ 变异体的**其余**判据必须仍然绿 ⇒ 证明上面的红是「正文规则没了」这个
#   行为红，不是「变异体编不过」这种假红。
if run_go_test "$MUT_PKG" 'TestClassifyStatusMatrix4xx' "变异臂·4xx 矩阵判据"; then
  ok "② 变异臂：变异体其余判据仍绿（红是行为红，不是编译红）"
else
  bad "② 变异臂：变异体的 4xx 矩阵判据也红了 ⇒ 变异打坏了别的东西（变异本身有问题）"
fi

# ═════════════════════════════════════════════════════════════════════
# 2. 被判定体
# ═════════════════════════════════════════════════════════════════════
if [[ "$SUBJECT" = "mutant" ]]; then
  SUBJ_PKG="$MUT_PKG"
  printf -- '--- 判定体：变异体（%s）---\n' "$SUBJ_PKG"
else
  SUBJ_PKG="./errorsx"
  printf -- '--- 判定体：%s ---\n' "$SUBJ_PKG"
fi

# ── 断言 ②：kind 存在、且**不是** KindRateLimit ─────────────────────
if grep -qE 'KindRateLimitObserved[[:space:]]+ErrorKind[[:space:]]*=[[:space:]]*"rate_limit_observed"' "$CLASSIFY"; then
  ok "② 独立 kind 存在（★ KindRateLimitObserved，★ 线值 rate_limit_observed）"
else
  bad "② 找不到 KindRateLimitObserved 的定义（★ 或线值不是 rate_limit_observed）"
fi
if grep -qE 'KindRateLimit[[:space:]]+ErrorKind[[:space:]]*=[[:space:]]*"rate_limit"' "$CLASSIFY"; then
  ok "②b 既有 KindRateLimit 仍是独立线值（★ 本轮**没有**改它的语义）"
else
  bad "②b KindRateLimit 的定义不见了（★ 本轮不该动它）"
fi

# ── 断言 ③：正文规则挂在「请求侧状态族」上 ─────────────────────────
RULE_RE='isRequestRejectionStatus\(status\)[[:space:]]*&&[[:space:]]*rateLimitBodyRe\.Match\(body\)'
if grep -qE "$RULE_RE" "$CLASSIFY"; then
  ok "③ 正文规则只挂在请求侧状态族上（★ isRequestRejectionStatus ∧ rateLimitBodyRe）"
else
  bad "③ 正文规则没挂在 isRequestRejectionStatus 上（★ 5xx / 401 / 403 的正文也会命中 ⇒ 误伤）"
fi

# ── 断言 ④：★ 两个分类器**逐字同判**（★ 不得分叉）────────────────
n_rules=$(grep -cE "$RULE_RE" "$CLASSIFY")
if [[ "$n_rules" -ge 2 ]]; then
  ok "④ 两个分类器逐字同判（★ 命中处 $n_rules 处 ⇒ ClassifyErrorWithBody 与 ClassifyResponseBody 不分叉）"
else
  bad "④ 正文规则只出现 $n_rules 处（★ 期望 ≥2 处 ⇒ 两个分类器必须同判）"
fi

# ── 断言 ⑤：★ 正文正则**不含 \b**（★ 跨平台）─────────────────────
# ★ BSD libc 的 regcomp **不认** \b（★ 2026-11-10 实测：★ macOS 上
#   [[ =~ ]] 对 \b 是 MISS、★ 而 grep -E 是 HIT ⇒ ⇒ 同一份规则在
#   两个平台上读数不同 ⇒ ★ 平台分叉）⇒ ⇒ 本仓的正则一律用
#   (^|[^A-Za-z0-9_])…([^A-Za-z0-9_]|$)。
re_block=$(awk '
  /^var rateLimitBodyRe = regexp\.MustCompile\(/ { f=1 }
  f { print }
  f && /^\)$/ { exit }
' "$CLASSIFY")
if [[ -z "$re_block" ]]; then
  bad "⑤ 抽不出正文正则的定义块（★ 抽取器失效 ⇒ 下面两条断言都是空的 ⇒ 绿着漏）"
else
  if printf '%s' "$re_block" | grep -q '\\b'; then
    bad "⑤ 正文正则里出现了反斜杠 b（★ BSD libc 的 regcomp 不认它 ⇒ 跨平台读数分叉）"
  else
    ok "⑤ 正文正则不含反斜杠 b（★ 用了可移植的 ASCII 词边界）"
  fi
  if printf '%s' "$re_block" | grep -q '\[^A-Za-z0-9_\]'; then
    ok "⑤b 正文正则用了可移植的 ASCII 词边界（★ [^A-Za-z0-9_]）"
  else
    bad "⑤b 正文正则里找不到可移植词边界（★ 期望 [^A-Za-z0-9_]）"
  fi
fi

# ── 断言 ⑥：★ 归责不惩罚节点 ──────────────────────────────────────
if grep -A14 'case KindRateLimitObserved:' errorsx/penalty.go 2>/dev/null | grep -qE 'PenaltyNone|PenaltyBinding'; then
  pen=$(grep -A14 'case KindRateLimitObserved:' errorsx/penalty.go | grep -oE 'PenaltyNone|PenaltyBinding' | head -1)
  if [[ "$pen" == "PenaltyNone" ]]; then
    ok "⑥ 归责是 PenaltyNone（★ 不拉黑、★ 不连败累计 ⇒ 不惩罚节点）"
  else
    bad "⑥ 归责是 PenaltyBinding（★ 那就继承了限流族的惩罚语义 ⇒ 与裁决相反）"
  fi
else
  bad "⑥ 在 errorsx/penalty.go 里找不到 KindRateLimitObserved 的归责分支"
fi

# ★ ★ 探针窗口要给够：★ 本仓的分支体**带长注释** ⇒ 窗口太小会读不到
#   那行 return ⇒ 判据**静默失效**（★ 上一次就是这里哑掉的）。
if grep -A25 'case KindRateLimitObserved:' errorsx/automatic_probe_policy.go 2>/dev/null | grep -q 'AutomaticProbeNone'; then
  ok "⑥b 不扇出自动探针（★ AutomaticProbeNone）"
else
  bad "⑥b 在 errorsx/automatic_probe_policy.go 里读不到 AutomaticProbeNone（★ 探针扇出会白打上游）"
fi

# ★ ★ 连败累计闸门：★ IsRequestSideError 是「共享节点健康状态写入」的
#   **唯一判据入口** ⇒ 不登记 ⇒ 连败累计 ⇒ 3 连败整凭据 cooling。
if grep -q 'kind == KindRateLimitObserved' errorsx/classify.go \
   && awk '/^func IsRequestSideError/,/^}/' errorsx/classify.go | grep -q 'KindRateLimitObserved'; then
  ok "⑥c 登记在 IsRequestSideError（★ 不写共享节点健康状态、★ 不累计连败）"
else
  bad "⑥c 没登记在 IsRequestSideError（★ 会进连败累计 ⇒ 3 连败整凭据 cooling）"
fi

# ── 断言 ⑦：★ 行为判据（★ 跑 Go 测试）──────────────────────────
# ★ 判据取自 errorsx 包自己的测试（★ classify_rate_limit_observed_test.go）⇒
# ⇒ 行为与实现同源、★ 不会漂。
if run_go_test_nonempty "$SUBJ_PKG" 'RateLimitObserved' "限流正文归因行为判据"; then
  ok "⑦ 行为判据绿（★ errorsx 的限流正文归因测试全过）"
else
  bad "⑦ 行为判据红（★ 空跑也算红：★ 判据面必须非空）"
fi

# ── 断言 ⑧：★ 4xx 矩阵门仍然绿（★ 两个分类器不许分叉）──────────
if run_go_test_nonempty "$SUBJ_PKG" 'Classify4xx|4xxMatrix|TestClassifyErrorWithBody' "4xx 矩阵判据"; then
  ok "⑧ 4xx 矩阵门仍绿（★ 两个分类器同判）"
else
  bad "⑧ 4xx 矩阵门红（★ 空跑也算红：★ 判据面必须非空）"
fi

# ── 断言 ⑨：★ 量具自己有牙（★ 变异臂，见上文第 1 节）────────────
# ★ 本节的结论已在第 1 节登记 ⇒ 这里只做一次收口汇总。

echo
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
exit 0
