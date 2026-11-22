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
  *"docker logs"*)      cat "$FAKE_LOGS" 2>/dev/null ;;
  *"psql"*)             printf '42\n' ;;
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

run_script() {  # run_script <日志夹具> <输出文件>；★ 打印退出码
  FAKE_LOGS="$1" PATH="$WORK/bin:$PATH" bash "$SCRIPT" --host fake --window 24h >"$2" 2>"$2.err"
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

# ── ③ 参数契约：★ --help 退 0、★ 未知参数退 1 ───────────────────────────
bash "$SCRIPT" --help >/dev/null 2>&1 || fail "--help 退出码非 0"
bash "$SCRIPT" --nope >/dev/null 2>&1 && fail "未知参数没有退 1"
pass "参数契约：--help 退 0、★ 未知参数退 1"

# ── ④ 覆盖期纪律（★ 静态）：★ 分母查询必须与覆盖期同起点 ────────────────
grep -q "State.StartedAt" "$SCRIPT" || fail "脚本没有取容器启动时刻（★ 覆盖期不可知）"
grep -q "greatest(now() - interval" "$SCRIPT" || fail "分母查询没有与覆盖期取同一起点（★ 分子分母会不同期）"
pass "覆盖期纪律：分母与分子同起点"

echo "✅ 治理链拦截构成口径自证通过"
