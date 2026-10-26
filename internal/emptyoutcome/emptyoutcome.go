// Package emptyoutcome is the single semantic table deciding whether an
// upstream exchange carried zero assistant output (Wave4-D2, 2026-09-22).
// Before this package the judgment lived in four places with diverging
// criteria (streaming/stream.go per-chunk gate, streaming/anthropic_stream.go
// chunkCount==0, streaming/responses_bridge.go IsAnthropicStreamEmpty,
// streaming/executors/empty_response.go non-stream copies); every empty-outcome
// classification now routes through here so a new vendor shape or a rule
// change lands in exactly one file.
//
// The one rule every shape shares — usage-only output is EMPTY: usage frames
// (prompt/completion token accounting), keepalives, role announcements and
// [DONE] never constitute semantic output, with or without a finish_reason.
// A tool call, a reasoning delta, audio, or any unrecognized payload-bearing
// block DOES constitute output (failover must not discard payloads the
// gateway cannot fully model — R16 policy).
package emptyoutcome

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// IsEmptyOutcome reports whether an upstream exchange must be surfaced as
// KindEmptyResponse so the executor fails over to the next candidate.
//
// emittedOutput: did any semantic assistant output reach the exchange
// (accumulated text/reasoning/tool calls, or the per-bridge chunk counter
// over semantic frames only).
//
// clientDisconnectPending is deliberately distinct from merely having a
// pending capturer. A capturer is also installed for ordinary requests so a
// later client disconnect can be replayed; it must not turn a clean upstream
// empty response into a success. Only a stream that actually observed a
// client write failure may preserve completed-replay semantics.
//
// Usage tokens never make an exchange non-empty: providers can report
// prompt/output accounting while returning no assistant content.
func IsEmptyOutcome(emittedOutput bool, clientDisconnectPending bool) bool {
	return !emittedOutput && !clientDisconnectPending
}

// ChatChunkHasOutput reports whether an OpenAI SSE chunk carries real
// user-facing content. Used by the empty-stream content-gate to decide
// whether to flush the buffer (real content seen) or fail over (zero
// content seen before [DONE]).
//
// "Real content" = delta.content != "" OR a tool_call with a non-empty name
// or arguments. Usage-only and role-only chunks (Type="usage" / first-chunk
// assistant role announcement) do NOT count.
//
// r1029 (2026-10-17): delta.reasoning_content used to count as output and no
// longer does. Measured on 154, request 623e36495abf1de578592fa90c78417a
// (glm-5.3, z-ai/glm-5.3-flash, max_tokens=8, stream=true): all 8 SSE frames
// carried only reasoning_content, content never appeared, and the budget went
// entirely to reasoning (usage: completion_tokens=8, reasoning_tokens=6) with
// finish_reason=length and no [DONE]. Counting reasoning as output set
// sawSemanticOutput, so the gate took the benign-close branch and synthesized
// a [DONE] — HTTP 200, finish_reason=length, empty content, which is exactly
// the client's empty_model_response. Reasoning models must not be failed over
// for thinking out loud: the thinking channel is relayed separately, and a
// long deliberation is not an assistant answer. Owner decision 2026-10-17.
//
// Magpie / ZCode empty_model_response parity (2026-10-17): MiniMax and
// similar relays emit nameless phantom tool_calls slots (empty id/name/
// arguments) with finish_reason=tool_calls. A non-empty tool_calls array
// alone must NOT count — otherwise the chat empty gate flushes "success"
// and the client sees empty_model_response while the executor never
// retries. Named tools and argument deltas (name often arrived earlier)
// still count.
//
// Returns false (no content) on parse errors — a malformed chunk is treated
// like an empty one so the gate keeps buffering and either hits the chunk/
// byte cap or [DONE] arrives with zero content → Resumable failover.
func ChatChunkHasOutput(payload string) bool {
	if payload == "" || payload == "[DONE]" {
		return false
	}
	chunk, err := ir.ParseOpenAIStreamChunk("data: " + payload + "\n\n")
	if err != nil || chunk == nil {
		return false
	}
	if chunk.Type == ir.ChunkTypeDone || chunk.Type == ir.ChunkTypeError {
		return false
	}
	if chunk.Delta == nil {
		return false
	}
	if chunk.Delta.Content != "" {
		return true
	}
	for _, tc := range chunk.Delta.ToolCalls {
		if tc.Name != "" || tc.Arguments != "" {
			return true
		}
	}
	if chunk.Delta.AudioDelta != nil &&
		(chunk.Delta.AudioDelta.Data != "" || chunk.Delta.AudioDelta.Transcript != "") {
		return true
	}
	return false
}

// ChatSemanticDelta reports whether payload is a valid OpenAI delta frame
// with at least one choice but no semantic output. It deliberately excludes
// usage, keepalive, malformed payloads, empty/missing choices, and active
// reasoning. Reasoning is progress while generating, even though reasoning
// alone is not a completed assistant answer (ChatChunkHasOutput).
func ChatSemanticDelta(payload string) bool {
	if payload == "" || payload == "[DONE]" {
		return false
	}
	var envelope struct {
		Choices json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil || len(envelope.Choices) == 0 {
		return false
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(envelope.Choices, &choices); err != nil || len(choices) == 0 {
		return false
	}
	chunk, err := ir.ParseOpenAIStreamChunk("data: " + payload + "\n\n")
	return err == nil && chunk != nil && chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil &&
		chunk.Delta.ReasoningContent == "" && !ChatChunkHasOutput(payload)
}

// Format classifies a non-stream upstream body by wire protocol.
type Format uint8

const (
	FormatUnknown Format = iota
	FormatChat
	FormatAnthropic
	FormatResponses
)

// ClassifyBody inspects a non-stream upstream body and reports its protocol
// format plus whether that body carries no semantic output. Unknown/malformed
// bodies report (FormatUnknown, false) — the empty gate must not fail over on
// a shape it cannot parse (R16).
func ClassifyBody(body []byte) (Format, bool) {
	if len(body) == 0 || !json.Valid(body) {
		return FormatUnknown, false
	}

	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return FormatUnknown, false
	}
	if raw, ok := envelope["choices"]; ok {
		return FormatChat, ChatChoicesIsEmpty(raw)
	}
	if raw, ok := envelope["content"]; ok {
		return FormatAnthropic, AnthropicContentIsEmpty(raw)
	}
	if raw, ok := envelope["output"]; ok {
		return FormatResponses, ResponsesOutputIsEmpty(raw)
	}
	return FormatUnknown, false
}

// ChatBodyIsEmpty reports whether a chat-completions body carries no output.
// Unknown-format bodies count as empty (the chat executor consumes OpenAI
// upstreams; a body without any known envelope key has nothing to convert).
func ChatBodyIsEmpty(body []byte) bool {
	if len(body) == 0 || !json.Valid(body) {
		return false
	}
	format, empty := ClassifyBody(body)
	return format == FormatUnknown || (format == FormatChat && empty)
}

func ChatChoicesIsEmpty(raw json.RawMessage) bool {
	var choices []struct {
		Message struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Function *struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	}
	// R16: malformed choices → not empty (aligned with the executors copy);
	// the converter/quality layers own semantics, the empty gate must not
	// fail over on a gateway-side parse gap.
	if json.Unmarshal(raw, &choices) != nil {
		return false
	}
	if len(choices) == 0 {
		return true
	}
	for _, choice := range choices {
		// r1029 (2026-10-17): message.reasoning_content no longer counts as
		// output — same rule and same 154 evidence as ChatChunkHasOutput. A
		// body whose entire completion went into reasoning (max_tokens
		// exhausted by deliberation, content never emitted) carries no
		// assistant answer and must fail over instead of reaching the client
		// as an empty success.
		//
		// Magpie parity: nameless phantom tool_calls do not constitute
		// agent-visible output (same rule as ChatChunkHasOutput). ID alone
		// without name/arguments is still a phantom slot.
		for _, tc := range choice.Message.ToolCalls {
			if tc.Function != nil && (tc.Function.Name != "" || tc.Function.Arguments != "") {
				return false
			}
		}
		if ChatContentHasOutput(choice.Message.Content) {
			return false
		}
	}
	return true
}

// EmptyReason names WHY an upstream body carried no assistant output.
//
// ★ R150 轮（2026-10-27）新增：分类「空」是一件事，**说清「为什么空」是另一件事**。
// 实测（本地 8782 网关 + nvidia 上游，provider 2452 / credential 2 /
// deepseek-ai/deepseek-v4.1-flash）：上游对同一条长提示词返回 **HTTP 200**，
// 但 `message.content` 为空、`usage.completion_tokens` 恰好等于请求的
// `max_tokens`、`usage.completion_tokens_details.reasoning_tokens` 也等于同一个数
// ⇒ **整个输出预算被推理吃掉、content 从未产出**。
//
// ⇒ 而服务端日志当时只记 `status=200` 与 `credential_id` ⇒ **运维无法区分**
// 「上游挂了」与「上游答了但预算被推理吃光」这两种完全不同的故障 ⇒
// ⇒ 本类型 + InspectEmptyBody 把这条证据做出来（**只加可观测性，不改任何路由行为**）。
type EmptyReason string

const (
	// EmptyReasonUnknown — body 不可解析或没有 usage 信息（保持原样，不臆测）。
	EmptyReasonUnknown EmptyReason = ""
	// EmptyReasonBudgetExhausted — 上游把输出预算（max_tokens）耗尽在推理上，
	// assistant content 从未产出。**这不是「上游不可用」**，而是「预算不够」。
	EmptyReasonBudgetExhausted EmptyReason = "budget_exhausted_by_reasoning"
	// EmptyReasonUpstreamEmpty — 上游没有产出任何东西（content 与 reasoning 皆空）。
	EmptyReasonUpstreamEmpty EmptyReason = "upstream_empty"
)

// EmptyUsage is the upstream accounting of an empty-bodied response.
type EmptyUsage struct {
	// FinishReason is choices[0].finish_reason ("length" ⇒ 预算耗尽)。
	FinishReason string
	// CompletionTokens is usage.completion_tokens.
	CompletionTokens int
	// ReasoningTokens is usage.completion_tokens_details.reasoning_tokens.
	ReasoningTokens int
	// PromptTokens is usage.prompt_tokens.
	PromptTokens int
	// TotalTokens is usage.total_tokens.
	TotalTokens int
}

// InspectEmptyBody extracts the upstream accounting of an empty chat body and
// classifies why it was empty.
//
// 判据（两条任一成立 ⇒ EmptyReasonBudgetExhausted）：
//
//	① finish_reason == "length" 且 completion_tokens > 0
//	   —— 上游自己说「我撞到长度上限了」；
//	② reasoning_tokens > 0 且 reasoning_tokens >= completion_tokens
//	   —— 全部产出都在推理通道里，正文通道为空。
//
// 两条都不成立且 body 可解析 ⇒ EmptyReasonUpstreamEmpty。
// body 不可解析 / 无 usage ⇒ EmptyReasonUnknown（**不臆测归因**）。
//
// ★ 注意口径：**本函数只做「归因说明」，不改变任何空/非空的判定** ——
// 空/非空仍由 ChatBodyIsEmpty / ChatChunkHasOutput 决定（那是 2026-10-17
// 属主裁决过的口径：推理不算 assistant 答案 ⇒ 必须 failover）。
func InspectEmptyBody(body []byte) (EmptyReason, EmptyUsage) {
	if len(body) == 0 || !json.Valid(body) {
		return EmptyReasonUnknown, EmptyUsage{}
	}
	var envelope struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens            int `json:"prompt_tokens"`
			CompletionTokens        int `json:"completion_tokens"`
			TotalTokens             int `json:"total_tokens"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return EmptyReasonUnknown, EmptyUsage{}
	}
	u := EmptyUsage{
		FinishReason:     "",
		CompletionTokens: envelope.Usage.CompletionTokens,
		ReasoningTokens:  envelope.Usage.CompletionTokensDetails.ReasoningTokens,
		PromptTokens:     envelope.Usage.PromptTokens,
		TotalTokens:      envelope.Usage.TotalTokens,
	}
	if len(envelope.Choices) > 0 {
		u.FinishReason = envelope.Choices[0].FinishReason
	}
	switch {
	case u.FinishReason == "length" && u.CompletionTokens > 0:
		return EmptyReasonBudgetExhausted, u
	case u.ReasoningTokens > 0 && u.ReasoningTokens >= u.CompletionTokens:
		return EmptyReasonBudgetExhausted, u
	case u.CompletionTokens > 0 || u.ReasoningTokens > 0 || u.FinishReason != "":
		return EmptyReasonUpstreamEmpty, u
	default:
		return EmptyReasonUnknown, u
	}
}

// ChatContentHasOutput judges message.content in both wire shapes: a plain
// string and a content-part array (multimodal / server-tool parts / refusal).
// Unknown part types count as output — semantic attribution belongs to the
// converter / IR layer (b2639182b), the empty gate must not reclassify
// payload-carrying bodies as empty because of its own parsing gaps.
func ChatContentHasOutput(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		return s != ""
	}
	if raw[0] == '[' {
		var parts []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			return true // array shape we cannot interpret — assume payload
		}
		for _, p := range parts {
			if p.Text != "" || p.Refusal != "" {
				return true
			}
			switch p.Type {
			case "", "text", "output_text", "refusal":
				// text-shaped: only non-empty text/refusal counts
			default:
				return true // unknown part type: payload we cannot represent
			}
		}
		return false
	}
	return false
}

func AnthropicContentIsEmpty(raw json.RawMessage) bool {
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return true
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				return false
			}
		case "thinking":
			// r1029 (2026-10-17): a thinking block is deliberation, not the
			// assistant answer — same rule as the chat legs. A response whose
			// content array carries only thinking (budget exhausted mid-
			// deliberation) reaches the client as empty content otherwise.
			//
			// redacted_thinking stays OUT below: it is emitted in place of the
			// model's reasoning by the provider and conventionally accompanies
			// the answer turn, so it is kept as output to avoid failing over on
			// a shape the gateway cannot fully model (R16).
		case "redacted_thinking", "server_tool_use", "web_search_tool_result":
			return false
		case "tool_use":
			return false
		default:
			// R16 (2026-09-12): unrecognized block types count as output so
			// unknown-only bodies reach the IR OnlyUnsupportedBlocks guard
			// (KindConversion, stage=gateway) instead of an empty-response
			// failover that demotes the provider for a gateway gap.
			if block.Type != "" {
				return false
			}
		}
	}
	return true
}

// AnthropicMessagesBodyIsEmpty identifies a syntactically valid native
// Messages response that carries no semantic assistant output, mirroring the
// block semantics of AnthropicContentIsEmpty so the executor-level failover
// and the terminal 502 classifier cannot disagree about the same body:
//   - tool_use / server_tool_use / web_search_tool_result / redacted_thinking
//     blocks always count as output (a tool call is actionable even with an
//     empty input object);
//   - a thinking block counts as output when either thinking text or a
//     signature is present;
//   - any block with an unrecognized type counts as output (R16, 2026-09-12):
//     the gateway cannot know whether e.g. container_upload carries payload,
//     and b2639182b's OnlyUnsupportedBlocks guard in WriteNonStreamResponse
//     owns the attribution (KindConversion, stage=gateway) — an empty-response
//     failover here would demote the provider for a gateway-side gap and
//     starve the guard of bodies. Mixed unknown+text stays non-empty via the
//     text branch.
//
// Two deliberate refinements over the bare content classifier, both in the
// safe direction for failover:
//   - the envelope must carry type=="message", so foreign JSON shapes are
//     never misjudged here;
//   - a message envelope with NO content key at all counts as empty — a 2xx
//     Messages response without content carries zero output by construction.
func AnthropicMessagesBodyIsEmpty(body []byte) bool {
	if len(body) == 0 || !json.Valid(body) {
		return false
	}

	var envelope struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Type != "message" {
		return false
	}
	if envelope.Content == nil {
		return true
	}

	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(envelope.Content, &blocks); err != nil || len(blocks) == 0 {
		return true
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				return false
			}
		case "thinking":
			// r1029 (2026-10-17): same rule as AnthropicContentIsEmpty and
			// the chat legs — deliberation is not the assistant answer.
		case "tool_use", "server_tool_use", "web_search_tool_result", "redacted_thinking":
			return false
		default:
			if block.Type != "" {
				return false
			}
		}
	}
	return true
}

func ResponsesOutputIsEmpty(raw json.RawMessage) bool {
	var output []struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Text string `json:"text"`
		} `json:"content"`
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
	}
	if json.Unmarshal(raw, &output) != nil || len(output) == 0 {
		return true
	}
	for _, item := range output {
		// R16: named output item types this classifier doesn't model
		// (web_search_call, mcp_call, ...) count as output — same policy as
		// the Anthropic block loop above.
		switch item.Type {
		case "function_call":
			if item.CallID != "" || item.Name != "" || item.Arguments != "" {
				return false
			}
		case "message", "reasoning":
			// fall through to the content/summary checks below
		default:
			if item.Type != "" {
				return false
			}
		}
		for _, content := range item.Content {
			if content.Text != "" {
				return false
			}
		}
		for _, summary := range item.Summary {
			if summary.Text != "" {
				return false
			}
		}
	}
	return true
}
