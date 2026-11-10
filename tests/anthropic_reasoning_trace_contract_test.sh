#!/usr/bin/env bash
# tests/anthropic_reasoning_trace_contract_test.sh —— F7「Anthropic 腿恢复
# reasoning trace 转发」契约门。
#
# ★ 背景（2026-11-10 属主裁决）：F7 登记项「Anthropic 腿要不要拿回 reasoning
#   trace」的裁决是**恢复转发**。此前：
#   · 非流式：`internal/ir/response.go` 的 `SerializeAnthropicResponse` 只走
#     `ir.Content`；OpenAI 系上游的 `message.reasoning_content` 只进
#     `ir.ReasoningContent`、**不**建 "thinking" 块 ⇒ 整条 trace 在 Anthropic
#     腿被静默丢弃（F7 的主缺口）。
#   · 流式：`domains/streaming/anthropic_stream.go` 的 Q2 桥把上游
#     `delta.reasoning_content` 转成 thinking 块 ⇒ 线上**已经**转发
#     （本门用那条既有钉桩证明它没被这次改动弄坏）。
#
# ★ 本门钉住的东西：
#   ① 非流式 Anthropic 序列化器**转发** reasoning trace（线形态
#      `{"type":"thinking","thinking":"<trace>"}`，排在内容数组最前；IR 无签名
#      时**不写** signature 字段）。
#   ② IR 里已有 "thinking" 块时**不重复**发第二块（那块带签名）。
#   ③ Anthropic→Anthropic 原样透传**逐字不变**。
#   ④ 流式把上游 reasoning delta 送成 thinking_delta。
#   ⑤ **OpenAI 腿逐字不变**（本次改动不许顺带碰它）。
#   ⑥ ★★ 变异臂：★ 把转发**打坏**（在临时副本里）⇒ 转发判据**必须转红**
#      ⇒ 封堵「判据瞎了 ⇒ 门永远绿」★。
#
# ★ 用法：
#   bash tests/anthropic_reasoning_trace_contract_test.sh
#       ⇒ 判真实树（默认）⇒ 正确树上应当 PASS=… FAIL=0、退出码 0。
#   bash tests/anthropic_reasoning_trace_contract_test.sh --subject mutant
#       ⇒ 判**变异体**（转发被打坏的那份）⇒ 转发判据必须红 ⇒ 退出码 ≠ 0。
#       ★ 这就是「门在变异下红」的可复现入口 ★。
#   bash tests/anthropic_reasoning_trace_contract_test.sh --impl <包目录>
#       ⇒ 把判据指向候选实现（相对仓库根或绝对路径）⇒ 用于「这个改动能不能
#         过门」的自测与变异测试（与 tests/scan_secrets_enumeration_contract_test.sh
#         的候选实现约定同款）。
#
# ★ 零仓库副作用：★ 变异体建在仓库内的临时包目录里（`go test` 需要它在模块
#   内才解析得到），EXIT trap 必删 ⇒ 正常跑完仓库是干净的。
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

SUBJECT="real"          # real | mutant —— 被判定的实现体
IMPL="internal/ir"      # 候选实现包（相对仓库根，或绝对路径）
MUT_DIR_REL="internal/ir_f7_mutation_probe"

usage() {
  sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --subject) SUBJECT="${2:-}"; shift 2 || exit 64 ;;
    --impl)    IMPL="${2:-}";    shift 2 || exit 64 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'unknown arg: %s\n' "$1" >&2; usage >&2; exit 64 ;;
  esac
done

case "$SUBJECT" in
  real|mutant) ;;
  *) printf 'FATAL: --subject 只接受 real|mutant（收到 %q）\n' "$SUBJECT" >&2; exit 64 ;;
esac

if [[ "$IMPL" = /* ]]; then
  IMPL_DIR="$IMPL"
  IMPL_PKG="$IMPL_DIR"
else
  IMPL_DIR="$REPO_ROOT/$IMPL"
  IMPL_PKG="./$IMPL"
fi
[[ -d "$IMPL_DIR" ]] || { printf 'FATAL: 实现目录不存在：%s\n' "$IMPL_DIR" >&2; exit 1; }

MUT_DIR="$REPO_ROOT/$MUT_DIR_REL"

cleanup() {
  cd / 2>/dev/null || true
  rm -rf "$MUT_DIR"
}
trap cleanup EXIT

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); printf 'OK   %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1" >&2; }

command -v go >/dev/null 2>&1 || { printf 'FATAL: PATH 上没有 go —— 本门跑不了\n' >&2; exit 64; }

# ── 判定器：跑一组聚焦用例，回 0 = 绿、回非 0 = 红 ───────────────────────
# $1 = 包目录（绝对路径或 ./ 相对路径），$2 = -run 正则，$3 = 标签
run_go_test() {
  local pkg="$1" run="$2" label="$3" out rc
  out="$(cd "$REPO_ROOT" && CGO_ENABLED=0 go test "$pkg" -run "$run" -count=1 2>&1)"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    printf -- '--- %s: go test %s -run %s ⇒ rc=%s ---\n' "$label" "$pkg" "$run" "$rc" >&2
    printf -- '%s\n' "$out" | tail -25 | sed 's/^/    /' >&2
  fi
  return $rc
}

REASONING_NONSTREAM_RUN='TestAnthropicReasoningTrace_NonStreamingForwardsReasoning|TestAnthropicReasoningTrace_NonStreamingReasoningOnly'
REASONING_DEDUP_RUN='TestAnthropicReasoningTrace_NonStreamingDoesNotDuplicateThinkingBlock|TestAnthropicReasoningTrace_NonStreamingAnthropicPassthroughUnchanged'
REASONING_STREAM_RUN='TestAnthropicReasoningTrace_StreamingForwardsReasoning|TestAnthropicReasoningTrace_StreamingForwardsReasoningAlias'
OPENAI_LEG_RUN='TestAnthropicReasoningTrace_OpenAILegUnchanged'

# ── 0. 造变异体：★ 把转发打坏的那份实现（临时包，EXIT trap 必删）─────────
rm -rf "$MUT_DIR"
mkdir -p "$MUT_DIR" || { printf 'FATAL: 建不出变异体目录 %s\n' "$MUT_DIR" >&2; exit 1; }
shopt -s nullglob
for f in "$IMPL_DIR"/*.go; do
  case "$f" in *_test.go) continue ;; esac
  cp "$f" "$MUT_DIR/" || { printf 'FATAL: 复制 %s 失败\n' "$f" >&2; exit 1; }
done
# 只带上本门判据用的那一个用例文件（不复制其它用例 ⇒ 变异体里只有 F7 判据）
cp "$REPO_ROOT/internal/ir/anthropic_reasoning_trace_test.go" "$MUT_DIR/" \
  || { printf 'FATAL: 复制 F7 用例文件失败\n' >&2; exit 1; }

MUT_SRC="$MUT_DIR/response.go"
if [[ ! -f "$MUT_SRC" ]]; then
  bad "变异体里没有 response.go —— 变异臂没得打"
else
  # 变异：把「转发 reasoning」那个 if 的守卫换成恒假 ⇒ 行为回到 F7 之前
  # （trace 不再被转发），但**其余逻辑一字不动** ⇒ 变异体的红必须是行为红，
  # 不是编译红。
  sed 's/if ir\.ReasoningContent != "" && !hasAnthropicThinkingBlock(ir\.Content) {/if false {/' \
    "$MUT_SRC" > "$MUT_SRC.tmp" && mv "$MUT_SRC.tmp" "$MUT_SRC"
  if grep -q 'if false {' "$MUT_SRC"; then
    ok "0. 变异体已就位（转发守卫置为恒假）"
  else
    bad "0. 变异没打上（守卫形态变了？）⇒ 变异臂是假的，本门必须红"
  fi
fi

# ── 1. ★★ 变异臂自检：★ 转发被打坏 ⇒ 转发判据必须转红 ★★ ─────────────────
# ★ 这一步是「判据有没有牙齿」的证明：★ 若变异体仍然判绿 ⇒ 判据瞎了 ⇒
#   本门对真实缺陷同样会瞎 ⇒ 门必须红（不许假绿）★。
if run_go_test "./$MUT_DIR_REL" "$REASONING_NONSTREAM_RUN" "变异臂·非流式转发判据"; then
  bad "① ★★ 变异臂：转发被打坏后判据仍然判绿 ⇒ 判据无牙齿（假绿）★★"
else
  ok "① 变异臂：转发被打坏 ⇒ 非流式转发判据转红（判据有牙齿）"
fi
# ★ 变异体的**其余**判据必须仍然绿 ⇒ 证明上面的红是「转发没了」这个行为红，
#   不是「变异体编不过」这种假红。
if run_go_test "./$MUT_DIR_REL" "$REASONING_DEDUP_RUN" "变异臂·去重判据"; then
  ok "② 变异臂：变异体其余判据仍绿（红是行为红，不是编译红）"
else
  bad "② 变异臂：变异体的去重/透传判据也红了 ⇒ 变异打坏了别的东西（变异本身有问题）"
fi

# ── 2. 被判定体 ────────────────────────────────────────────────────────────
if [[ "$SUBJECT" = "mutant" ]]; then
  SUBJ_DIR="$MUT_DIR"
  SUBJ_PKG="./$MUT_DIR_REL"
  printf -- '--- 判定体：变异体（%s）---\n' "$SUBJ_PKG"
else
  SUBJ_DIR="$IMPL_DIR"
  SUBJ_PKG="$IMPL_PKG"
  printf -- '--- 判定体：%s ---\n' "$SUBJ_PKG"
fi

# ── 3. 非流式：Anthropic 序列化器必须转发 reasoning trace ────────────────
if run_go_test "$SUBJ_PKG" "$REASONING_NONSTREAM_RUN" "非流式转发判据"; then
  ok "③ 非流式 Anthropic 序列化器转发了 reasoning trace"
else
  bad "③ 非流式 Anthropic 序列化器**没有**转发 reasoning trace"
fi

# ── 4. 非流式：已有 thinking 块时不许重复；Anthropic 原样透传不许变 ───────
if run_go_test "$SUBJ_PKG" "$REASONING_DEDUP_RUN" "去重/透传判据"; then
  ok "④ 非流式：已有 thinking 块时不重复发块，Anthropic 原样透传逐字不变"
else
  bad "④ 非流式：thinking 块重复了，或 Anthropic 原样透传被改动了"
fi

# ── 5. 流式：上游 reasoning delta 必须以 thinking_delta 到达客户端 ────────
if run_go_test "$SUBJ_PKG" "$REASONING_STREAM_RUN" "流式转发判据"; then
  ok "⑤ 流式：上游 reasoning delta 以 thinking_delta 转发"
else
  bad "⑤ 流式：reasoning delta 没有以 thinking_delta 转发"
fi

# ── 6. ★ OpenAI 腿逐字不变（★ 判真实树：★ 这条腿本次不许被顺带碰过 ★）──
if run_go_test "./internal/ir" "$OPENAI_LEG_RUN" "OpenAI 腿判据"; then
  ok "⑥ OpenAI 腿未变（reasoning_content 逐字不变）"
else
  bad "⑥ OpenAI 腿被改动了（reasoning_content 变了）"
fi

# ── 7. 线上 Anthropic 流式桥（Q2）仍把 reasoning_content 转成 thinking 块 ─
# ★ 这条跑的是**生产代码**（domains/streaming 的 Q2 桥）+ 它自带的既有钉桩
#   ⇒ 证明「Anthropic 腿的流式转发」在线上确实成立，且没被这次改动弄坏。
if run_go_test "./domains/streaming" 'TestQ2NativeReasoningContentStreamsAsThinkingBlock' "线上 Q2 流式桥"; then
  ok "⑦ 线上 Q2 流式桥把 reasoning_content 转成 thinking 块"
else
  bad "⑦ 线上 Q2 流式桥不再把 reasoning_content 转成 thinking 块"
fi

echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]
