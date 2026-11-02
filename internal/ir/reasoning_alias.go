package ir

import "encoding/json"

// 本文件处理 **Ollama OpenAI-compat** 方言里 thinking trace 的名字与取值。
//
// ★ Ollama 用的那个名字是 `reasoning`（OpenAI 标准方言用 `reasoning_content`）。
//   2026-11-02 第十六轮（F-9 死代码清理）把同名常量 `reasoningAliasField`
//   删掉了：它零引用 ⇒ 取值直接走 reasoningAliasString 的实参。
//
// OpenAI's Chat Completions dialect carries the trace in
// `choices[].message.reasoning_content` (non-stream) and
// `choices[].delta.reasoning_content` (stream). Ollama's `/v1` compatibility
// layer instead emits the very same trace under the shorter name
// `reasoning` — in *both* shapes:
//
//	POST http://<host>:11434/v1/chat/completions  (stream=false)
//	{"choices":[{"message":{"role":"assistant","content":"",
//	 "reasoning":"Thinking Process:\n\n1. **Analyze…"},"finish_reason":"length"}]}
//
//	POST http://<host>:11434/v1/chat/completions  (stream=true)
//	data: {"choices":[{"delta":{"role":"assistant","content":"",
//	 "reasoning":"Thinking"},"finish_reason":null}]}
//
// Measured on Ollama 0.40.11 (local /v1 endpoint, model qwen3.5:4b,
// 2026-11-02): the `message` object carries exactly the keys
// {role, content, reasoning} — `reasoning_content` is **absent**, so the
// generic OpenAI parser silently dropped the whole trace.
//
// Consequences of the gap (before this file existed):
//
//  1. ★★ 订正（2026-11-02 第十六轮 · F-8）：原文写的是「The thinking trace
//     never reached the client」——**这句话只对 Anthropic 腿成立**，对
//     OpenAI 腿**不成立**：
//     · OpenAI 腿把**上游原始 body / 原始 SSE 行**原样转发给客户端
//       （domains/streaming/executors/executor_chat.go 的
//       params.W.Write(respBody)；domains/streaming/stream.go 的
//       writeClientLine）⇒ trace 从来没有从**客户端的视野**里消失过。
//     · ⇒ alias 改的是**网关内部视图**（IR）：读 `reasoning_content` 的
//       OpenAI SDK 客户端仍然看不到 trace（上游给的名字是 `reasoning`），
//       而**走 IR 的 Responses 桥**收益为真（IR 看得见 trace）。
//     · Anthropic 腿**仍然丢**（非流式序列化不读 IR 的 ReasoningContent，
//       流式桥读的是原始 wire key `reasoning_content`）⇒ 那是兄弟档 F7，
//       本轮**未修**（它要的是产品侧裁决：是否承诺向 Anthropic 客户端
//       恢复转发）。
//  2. `usage.completion_tokens_details.reasoning_tokens` is absent for
//     Ollama, so `emptyoutcome.InspectEmptyBody` could only attribute the
//     empty answer via criterion ① (finish_reason == "length").
//
// ★ 订正（2026-11-02 12h 审计轮 R3）：本文件原先在这里写着「this alias
//   does not change any empty/non-empty decision」——**那句话不准确**，
//   本轮把它改掉：
//
//   · **终判不变**：推理仍然**不是** assistant 答案 ⇒ reasoning-only 的
//     响应仍然是空产出，仍然 failover（2026-10-17 属主裁决不动；
//     internal/emptyoutcome 的 ChatBodyIsEmpty / ChatChunkHasOutput /
//     sawSemanticOutput 三处判据都不把 reasoning 算作输出）。
//   · **时点变了**：`emptyoutcome.ChatSemanticDelta` 的契约是「**排除
//     正在进行的 reasoning**」（它判 `ReasoningContent == ""`）。在本文件
//     之前，IR 看不见 Ollama 的 `reasoning` ⇒ 那个字段恒为空 ⇒ Ollama 的
//     reasoning-only delta 被当成「空 delta」⇒ 计入
//     `consecutiveEmptyDeltas` ⇒ 达到阈值（默认 3，
//     LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS）就**提前**判空并
//     failover（客户端 0 字节）。
//     本文件之后，IR 看得见 trace ⇒ 那条「排除正在进行的 reasoning」的
//     契约对 Ollama **真的生效** ⇒ reasoning-only 的 burst 不再触发
//     提前判空 ⇒ 空判据改在 [DONE] 处给出。
//   · ⇒ **净效果是把 Ollama 对齐到标准 OpenAI `reasoning_content` 方言的
//     既有行为**（该方言一直如此），不是新造行为。代价是：Ollama 的
//     reasoning-only 流现在会先在门限处把缓冲的 trace 刷给客户端，再在
//     [DONE] 处 failover；**而这条代价对每一个标准 reasoning 方言本来就
//     存在**（不是本 alias 引入的），已登记为待下一轮裁决项。
//
//   · 门：internal/emptyoutcome 的 reasoning-delta 一致性测试钉住两种
//     方言（`reasoning` 与 `reasoning_content`）对 ChatSemanticDelta 的
//     判定**必须一致**——这正是「alias 把两个方言对齐」这件事的可执行口径。
//
// What this helper deliberately does NOT do:
//
//   - It does not make reasoning count as assistant output. The 2026-10-17
//     owner decision (see internal/emptyoutcome) stands: a reasoning delta
//     is progress, not an answer, so a reasoning-only body is still an
//     empty outcome and still fails over. Only the **relay** of the trace
//     is restored here.
//   - It does not touch the Responses API, where top-level `reasoning` is a
//     *configuration object* (`{"effort":"high"}`) and never a trace. That
//     parser is a different code path (parse_responses.go) and is not
//     affected by this helper.
//
// The value is read as json.RawMessage rather than a typed string so that a
// provider that echoes back an **object** under `reasoning` (e.g.
// `{"reasoning":{"effort":"high"}}`) does not make the whole envelope fail
// to unmarshal — a plain `string` field would abort the parse of the entire
// response.

// reasoningAliasString extracts a reasoning trace from a raw JSON value that
// may be either a string (Ollama /v1 dialect — the trace) or an object
// (Responses-style `reasoning` config — not a trace, ignored).
//
// Returns "" for a missing, null, non-string, or empty value.
func reasoningAliasString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
