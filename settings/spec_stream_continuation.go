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
			Default:         false,
			Description:     "断流原地续问（默认关闭）：仅经能力白名单确认的官方上游，且未开始工具调用时，以已发文本续问一次。关闭时按 eof_without_done 终态化。",
			DescriptionLong: "显式开启后仍须匹配官方主机与模型白名单：api.deepseek.com 的 deepseek-chat（/beta + prefix:true）；api.moonshot.cn/api.moonshot.ai 的 moonshot-v1-8k/32k/128k（partial:true）。通用 OpenAI 兼容中转、其他模型、Anthropic/Responses 线格式不启用。SSE 自然结束但无终态且已发正文、未开始任何工具调用时，同候选续问至多 1 次；已发工具调用（含其他 choice 和旧 function_call）禁止续问。续问失败或再次断流按 eof_without_done 终态化。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}
