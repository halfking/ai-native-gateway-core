package settings

// CategoryStreamContinuation groups the in-place stream-continuation survival
// settings (P0-3, 2026-10-14).
const CategoryStreamContinuation Category = "stream_continuation"

// KeyStreamContinuationEnabled toggles the eof_without_done in-place
// continuation: when an OpenAI-chat-wire upstream drops the stream after the
// client has already seen committed text (no finish_reason, no [DONE]), the
// gateway re-issues the SAME candidate once with the accumulated text as a
// trailing assistant prefill message and splices the continuation SSE into
// the same outbound pipe. false restores the legacy §11.6 behaviour
// (structured error envelope + synthesized [DONE] + terminal).
const KeyStreamContinuationEnabled = "survival.stream_continuation_enabled"

// StreamContinuationSpecs returns the platform-scoped stream-continuation
// specs. Follows the spec_error_probe.go / spec_models.go shape: keys MUST be
// registered here, otherwise settings.GetPlatformBool silently falls back to
// the default and the toggle can never be flipped (r1027 lesson).
func StreamContinuationSpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeyStreamContinuationEnabled,
			EnvName:         EnvNameAuto(KeyStreamContinuationEnabled),
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStreamContinuation,
			Default:         true,
			Description:     "断流原地续问（P0-3）：上游流式响应中途断掉（无 finish_reason/[DONE]）且客户端已收到部分正文时，同一候选以已发文本为 assistant prefill 续问一次并无缝拼接。关闭即回到 eof_without_done 现有终态化行为。",
			DescriptionLong: "上游为 OpenAI chat completions 线格式的流式请求生效。触发条件：SSE 循环自然结束但无终态标记、且已有正文转发给客户端。动作：原出站体 messages 追加一条 assistant 消息（content=已累计正文纯文本，reasoning/tool_call 片段不进 prefill），同候选同上游重发一次（每请求预算恰好 1 次），续问响应帧喂进同一条出站管道并抑制首帧 role 重复。续问腿再断流或出错则按现有 eof_without_done 语义终态化。anthropic/responses 线格式上游的续问暂不覆盖。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}
