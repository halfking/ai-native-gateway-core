#!/bin/bash
# verify-governance-intercept-composition.sh —— 治理链拦截构成取数口径的**自证**
# （2026-11-23 N2 配套）
#
# ★ 为什么要有这个自证（★ 口径类脚本最容易「跑得通但口径是错的」）：
#
#	★ ① **泄露边界**：★ 这个脚本会读**生产归因日志** ⇒ ★ 它一旦把原始行打出来
#	  ⇒ 用户原文/命中片段就进到了终端与 CI 日志里 ⇒ ★ 必须钉死「只出聚合计数」。
#	★ ② **覆盖期纪律**：★ `docker logs` 按容器存 ⇒ ★ 容器一重启，★ 更早的日志
#	  就没了 ⇒ ★ 若分母仍按窗口算 ⇒ ★ **分子分母不同期** ⇒ ★ 比率是个没有意义的数字。
#	  ⇒ 口径必须**自己**把分母缩到与分子同一覆盖期。
#
# ★ 做法：★ 用一个**假 ssh**（★ 放在 PATH 前面）把三类远端读数（★ 容器名 /
# ★ 启动时刻 / ★ 日志行 / ★ 请求数）全部喂成固定值 ⇒ ★ 端到端跑真脚本、
# ★ 断言它的行为。★ ★ 对照臂：★ 换成「含泄露字段」的日志行 ⇒ ★ 脚本必须**拒绝出数**。
#
# ★ 用法：bash scripts/audit/verify-governance-intercept-composition.sh
# ★ 退出码：0 = 自证通过；1 = 自证失败

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/../governance-intercept-composition.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/lgw-intercept-verify.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "★ 自证失败: $*" >&2; exit 1; }
pass() { echo "✅ $*"; }

# ── 假 ssh ────────────────────────────────────────────────────────────────
# ★ $1 = 主机名、★ $2 = 远端命令；★ 按命令里的关键词分派。
mkdir -p "$WORK/bin"
cat > "$WORK/bin/ssh" <<'FAKE_SSH'
#!/bin/bash
cmd="${2:-}"
case "$cmd" in
  *"docker ps"*)        printf 'llm-gateway-fake-a\tUp 10 minutes (healthy)\n' ;;
  *"docker inspect"*)   printf '2026-11-22T21:37:59.280507336Z\n' ;;
  *"docker logs"*)
    # ★ ★ 假 ssh 必须**模拟真机的 grep 过滤**（★ 真机上脚本是两次取数：
    # ★   一次 grep 'short-circuit'、★ 一次 grep 'PI verdict divergence observed'）
    # ★ ⇒ 不模拟的话 ⇒ ★ RAW_DIV 会拿到**全部行** ⇒ ★ 形态 b 的计数被夹具污染
    # ★   ⇒ ★ 断言「形态 b = 1」永远红 ⇒ ★ 量具失真。
    case "$cmd" in
      *"PI verdict divergence observed"*) grep -F 'PI verdict divergence observed' "$FAKE_LOGS" 2>/dev/null ;;
      *"short-circuit"*)                   grep -F 'short-circuit' "$FAKE_LOGS" 2>/dev/null ;;
      *)                                   cat "$FAKE_LOGS" 2>/dev/null ;;
    esac ;;

  *"psql"*)
    # ★ 治理链开关查询（★ settings_kv）⇒ ★ 与「请求总数」是**两种**用途
    #   ⇒ ★ 假 ssh 必须分开喂（★ 否则两边读到同一个值）。
    case "$cmd" in
      *"settings_kv"*)
        # ★ 分隔符必须是 **|**（★ 真机用 psql -F'|'）⇒ ★ 否则 python 解析不出 key
        if [ -n "$FAKE_GOV_SW" ]; then printf '%s\n' "$FAKE_GOV_SW"; else printf 'security.enabled|true\nsecurity.mode|"observe"\n'; fi ;;
      *"prompt_injection_policies"*)
        # ★ 形态 b 可达性诊断的取数（★ 分隔符 **:** ⇒ ★ 真机用 psql -F':'）
        if [ -n "$FAKE_PI_REACH" ]; then printf '%s\n' "$FAKE_PI_REACH"; else printf 'policy:default:observe:6:10\nmatrix:low:log:log\nmatrix:medium:warn:replace\nmatrix:high:warn:reject\nmatrix:critical:warn:block\n'; fi ;;
      *) printf '42\n' ;;
    esac ;;
  *)                    : ;;
esac
exit 0
FAKE_SSH
chmod +x "$WORK/bin/ssh"

# ── 夹具 ──────────────────────────────────────────────────────────────────
# ★ 干净夹具：★ 只有形态字段 + 一枚**不该被打印**的 canary（★ 放在「不会被聚合
# ★ 的字段」上）⇒ ★ 断言 canary 不出现在输出里。
CANARY="CANARY-USER-TEXT-DO-NOT-PRINT"
cat > "$WORK/logs_clean.jsonl" <<EOF
{"timestamp":"2026-11-22T21:41:17.716436421Z","msg":"v2 dispatch gate: short-circuit","path":"/v1/chat/completions","status":403,"decision_kind":"block","decision_reason":"[prompt_injection] critical threat detected","decision_deny_plugins":"prompt_injection","decision_verdicts":"prompt_injection(deny,sev3,...)","user":"$CANARY"}
{"timestamp":"2026-11-22T21:41:18.070702238Z","msg":"v2 dispatch gate: short-circuit","path":"/v1/chat/completions","status":403,"decision_kind":"block","decision_reason":"[prompt_injection] critical threat detected","decision_deny_plugins":"prompt_injection","decision_verdicts":"prompt_injection(deny,sev3,...)","user":"$CANARY"}
EOF

# ★ 泄露夹具：★ 含 matched_pattern ⇒ ★ 脚本必须拒绝出数（★ 退出码 1）。
cat > "$WORK/logs_leaky.jsonl" <<EOF
{"timestamp":"2026-11-22T21:41:17.716436421Z","msg":"v2 dispatch gate: short-circuit","status":403,"decision_kind":"block","decision_reason":"[prompt_injection] critical threat detected","decision_deny_plugins":"prompt_injection","matched_pattern":"ignore previous"}
EOF

# ★ 分歧夹具（N8）：★ **两种形态各一条**、★ 放在同一个文件里
#   —— ★ 真实日志就是混在一起的 ⇒ ★ 顺带验证「★ 假 ssh 的 grep 过滤」
#      （★ 真机上 RAW 与 RAW_DIV 是两次不同过滤的取数）。
#   形态 a（★ 拦截侧）：★ 同一份 decision_verdicts 里 PI 家族方向不一致
#     （★ 词表 deny sev3 + enhanced allow sev2）⇒ ★ 请求被拦 ⇒ ★ 走 short-circuit。
#   形态 b（★ 放行侧）：★ enhanced 判 medium ⇒ severity 1 ⇒ ★ 低于 BlockThreshold=2
#     ⇒ ★ 链子不拦 ⇒ ★ 请求放行 ⇒ ★ **只有新观测点会记录它**。
cat > "$WORK/logs_div.jsonl" <<EOF
{"timestamp":"2026-11-22T21:42:00.000000000Z","msg":"v2 dispatch gate: short-circuit","path":"/v1/chat/completions","status":403,"decision_kind":"block","decision_reason":"[prompt_injection] critical threat detected","decision_deny_plugins":"prompt_injection","decision_verdicts":"prompt_injection(deny,sev3,...) prompt_injection_enhanced(allow,sev2,...) security.legacy(allow,sev0,...)","user":"$CANARY"}
{"timestamp":"2026-11-22T21:42:01.000000000Z","msg":"v2 dispatch: PI verdict divergence observed","path":"/v1/chat/completions","pi_divergence":"true","pi_divergence_denied":"prompt_injection_enhanced","pi_divergence_allowed":"prompt_injection","user":"$CANARY"}
EOF

run_script() {  # run_script <日志夹具> <输出文件>；★ 打印退出码
  FAKE_LOGS="$1" PATH="$WORK/bin:$PATH" bash "$SCRIPT" --host fake --window 24h >"$2" 2>"$2.err"
  echo $?
}

run_script_gov() {  # run_script_gov <日志夹具> <输出文件> <治理链开关串>
  FAKE_LOGS="$1" FAKE_GOV_SW="$3" PATH="$WORK/bin:$PATH" \
    bash "$SCRIPT" --host fake --window 24h >"$2" 2>"$2.err"
  echo $?
}

run_script_reach() {  # run_script_reach <日志夹具> <输出文件> <可达性取数串>
  FAKE_LOGS="$1" FAKE_PI_REACH="$3" PATH="$WORK/bin:$PATH" \
    bash "$SCRIPT" --host fake --window 24h >"$2" 2>"$2.err"
  echo $?
}

# ── ① 干净夹具：★ 口径跑通、★ 数字正确、★ canary 不外泄 ────────────────
rc="$(run_script "$WORK/logs_clean.jsonl" "$WORK/out_clean")"
[[ "$rc" == "0" ]] || fail "干净夹具下脚本退出码 ${rc}（★ 期望 0）"
out="$(cat "$WORK/out_clean")"
grep -q "拦截次数: 2" <<<"$out" || fail "拦截次数不是 2：$out"
grep -q "日志覆盖起点" <<<"$out" || fail "输出里没有『日志覆盖起点』（★ 覆盖期必须披露）"
grep -q "2026-11-22T21:37:59" <<<"$out" || fail "覆盖起点不是容器启动时刻（★ 口径没取 State.StartedAt）"
grep -q "同期请求总数: 42" <<<"$out" || fail "请求总数没取到（★ 期望假 ssh 喂的 42）"
grep -q "prompt_injection" <<<"$out" || fail "拦截构成里没有 deny 插件名"
grep -q "2026-11-22T21:41:17" <<<"$out" || fail "输出里没有拦截时刻（★ 探针/自然拦截要靠它区分）"
if grep -q "$CANARY" <<<"$out"; then
  fail "★ canary 出现在输出里 ⇒ ★ 脚本打印了原始日志行（★ 泄露边界破了）"
fi
pass "干净夹具：计数/覆盖期/分母/构成/时刻都对、★ 且没有打印原始行"

# ── ② 泄露夹具（★ 对照臂）：★ 必须拒绝出数 ──────────────────────────────
rc="$(run_script "$WORK/logs_leaky.jsonl" "$WORK/out_leaky")"
[[ "$rc" == "1" ]] || fail "泄露夹具下脚本退出码 ${rc}（★ 期望 1＝拒绝出数）"
if grep -q "matched_pattern" "$WORK/out_leaky"; then
  fail "★ 泄露夹具下脚本把 matched_pattern 打了出来（★ 泄露边界破了）"
fi
pass "对照臂：日志含匹配片段时脚本拒绝出数、★ 且不打印该片段"

# ── ③ 分歧两种形态（N8）：★ 拦截侧（形态 a）+ 放行侧（形态 b）各计一次 ──
# ★ 用 --json 精确断言（★ 纯文本 grep 会把两个「分歧次数」串味）。
command -v python3 >/dev/null 2>&1 || fail "缺少 python3（★ 本自证要用它解析 --json 输出）"
FAKE_LOGS="$WORK/logs_div.jsonl" PATH="$WORK/bin:$PATH" \
  bash "$SCRIPT" --host fake --window 24h --json >"$WORK/out_div.json" 2>"$WORK/out_div.json.err" \
  || fail "分歧夹具下脚本退出码非 0（★ 期望 0）"
# ★ 干净夹具也要跑一份 --json（★ 供下面的**反向断言**用）。
FAKE_LOGS="$WORK/logs_clean.jsonl" PATH="$WORK/bin:$PATH" \
  bash "$SCRIPT" --host fake --window 24h --json >"$WORK/out_clean.json" 2>"$WORK/out_clean.json.err" \
  || fail "干净夹具 --json 退出码非 0（★ 期望 0）"

# ★ ★ 断言必须**自己传播失败**：★ 本脚本没有 `set -e`（★ 用 `set -uo pipefail`）
#   ⇒ ★ python 断言失败会被 shell 忽略 ⇒ ★ 脚本照样往下跑、照样打印「✅ 通过」、
#   ★ 退出码还是 0 ⇒ ★ **量具自己失真**（★ 变异自证时才发现）⇒ ★ 这里显式 `|| fail`。
python3 - "$WORK/out_div.json" "$WORK/out_clean.json" <<'PY' || fail "★ 分歧双形态断言不通过（★ 见上方 stderr）"
import json, sys
d = json.load(open(sys.argv[1]))
clean = json.load(open(sys.argv[2]))
def chk(cond, msg):
    if not cond:
        print("★ 自证失败: " + msg, file=sys.stderr)
        sys.exit(1)
# ★ 形态 a 的那行是 short-circuit ⇒ ★ 拦截次数 = 1（★ 形态 b 那行不是 ⇒ 不计）。
chk(d["intercepts"] == 1, "拦截次数应为 1（形态 a 那行），实为 %r" % d["intercepts"])
# ★ 形态 a：★ 拦截侧分歧 = 1（★ 从既有 decision_verdicts 解析）。
chk(d["pi_divergences_block_side"] == 1,
    "形态 a（拦截侧）分歧应为 1，实为 %r" % d["pi_divergences_block_side"])
# ★ 形态 b：★ 放行侧分歧 = 1（★ 只由新观测点产出）。
chk(d["pi_divergences_pass_side"] == 1,
    "形态 b（放行侧）分歧应为 1，实为 %r" % d["pi_divergences_pass_side"])
# ★ ★ 反向断言（★ 这条是**变异抓手**）：★ 干净夹具里每行只有**单个插件的 deny**
#   ⇒ ★ **不构成分歧** ⇒ ★ 形态 a 必须为 0。
#   ★ ⇒ 若有人把判定改成「只要有 deny 就算分歧」⇒ ★ 这里立刻红。
chk(clean["pi_divergences_block_side"] == 0,
    "干净夹具（单插件 deny）不应算分歧，实为 %r" % clean["pi_divergences_block_side"])
chk(clean["pi_divergences_pass_side"] == 0,
    "干净夹具不应有放行侧分歧，实为 %r" % clean["pi_divergences_pass_side"])
# ★ 形态 b 的方向：★ enhanced 认为该拦、★ 词表认为该放。
pairs = d["pi_divergence_pairs"]
chk(any("prompt_injection_enhanced" in k and "prompt_injection" in k for k in pairs),
    "形态 b 的分歧方向对不上: %r" % pairs)
# ★ 形态 a 的方向：★ 词表 deny ⇄ enhanced allow。
apairs = d["pi_divergence_block_side_pairs"]
chk(any("prompt_injection" in k and "prompt_injection_enhanced" in k for k in apairs),
    "形态 a 的分歧方向对不上: %r" % apairs)
PY
pass "分歧双形态：★ 拦截侧 1 + 放行侧 1、★ 方向正确、★ 且假 ssh 的 grep 过滤生效"

# ── ④ 治理链期望状态（★ 「零拦截」的**前置条件**）──────────────────────────
# ★ 正向：★ 链开着 ⇒ 必须披露 security.enabled、★ 且明说「零拦截可按无命中读」。
out="$(cat "$WORK/out_clean")"
grep -q "security.enabled=true" <<<"$out" || fail "输出里没有治理链开关（★ 零拦截的前置条件必须披露）"
grep -q "链是开着的" <<<"$out" || fail "链开着时必须明说『零拦截可按无命中读』"
# ★ 反向（★ 对照臂）：★ 链被关 ⇒ 必须打警示 ⇒ ★ 否则读者会把「链关了」
#   读成「没有攻击」（★ 这正是本节存在的理由）。
rc="$(run_script_gov "$WORK/logs_clean.jsonl" "$WORK/out_gov_off" "security.enabled|false")"
[[ "$rc" == "0" ]] || fail "对照臂（链关着）退出码 ${rc}（★ 期望 0）"
outoff="$(cat "$WORK/out_gov_off")"
grep -q "security.enabled=false" <<<"$outoff" || fail "对照臂输出里没有 security.enabled=false"
grep -qF "解读为「没有攻击」" <<<"$outoff" || \
  fail "★ 链被关时脚本没有打警示 ⇒ ★ 读者会把『链关了』读成『没有攻击』"
pass "治理链期望状态：★ 链开/链关两态都披露、★ 链关时必打警示"

# ── ⑤ 形态 b 可达性判定（★ 「零分歧」是策略使然、★ 还是观测点失效）───────
# ★ 正向：★ 当前策略（observe + medium/high/critical 的 observe_action=warn）
#   ⇒ ★ 形态 b **不可达** ⇒ ★ 脚本必须明说「零分歧是策略使然、不是观测点失效」。
out="$(cat "$WORK/out_clean")"
grep -q "形态 b \*\*当前不可达\*\*" <<<"$out" || \
  fail "当前策略下形态 b 不可达、★ 但脚本没这么判（★ 判定逻辑错了）"
grep -qF "不是观测点失效" <<<"$out" || \
  fail "脚本没有把『零分歧』解释成『策略使然』⇒ ★ 读者会误判观测点失效"
# ★ 反向（★ 对照臂）：★ 把 medium 的 observe_action 改成 reject
#   ⇒ ★ enhanced deny sev1 ⇒ ★ 低于 BlockThreshold ⇒ ★ **形态 b 变可达**
#   ⇒ ★ 脚本必须改口（★ 这正是「策略一变、观测点就活」的可观测性）。
rc="$(run_script_reach "$WORK/logs_clean.jsonl" "$WORK/out_reach_on" \
  'policy:default:observe:6:10
matrix:low:log:log
matrix:medium:reject:replace
matrix:high:warn:reject
matrix:critical:warn:block')"
[[ "$rc" == "0" ]] || fail "对照臂（形态 b 可达）退出码 ${rc}（★ 期望 0）"
outon="$(cat "$WORK/out_reach_on")"
grep -q "形态 b \*\*当前可达\*\*" <<<"$outon" || \
  fail "★ medium 动作配成 reject 后形态 b 变可达、★ 但脚本仍判不可达 ⇒ ★ 判定逻辑错了"
pass "形态 b 可达性：★ 不可达/可达两态都判对、★ 且区分『策略使然』与『观测点失效』"

# ── ⑥ 参数契约：★ --help 退 0、★ 未知参数退 1 ───────────────────────────
bash "$SCRIPT" --help >/dev/null 2>&1 || fail "--help 退出码非 0"
bash "$SCRIPT" --nope >/dev/null 2>&1 && fail "未知参数没有退 1"
pass "参数契约：--help 退 0、★ 未知参数退 1"

# ── ⑦ 覆盖期纪律（★ 静态）：★ 分母查询必须与覆盖期同起点 ────────────────
grep -q "State.StartedAt" "$SCRIPT" || fail "脚本没有取容器启动时刻（★ 覆盖期不可知）"
grep -q "greatest(now() - interval" "$SCRIPT" || fail "分母查询没有与覆盖期取同一起点（★ 分子分母会不同期）"
pass "覆盖期纪律：分母与分子同起点"

echo "✅ 治理链拦截构成口径自证通过"
