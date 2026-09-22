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
// "Real content" = delta.content != "" OR delta.reasoning_content != ""
// OR delta.tool_calls non-empty. Usage-only and role-only chunks
// (Type="usage" / first-chunk assistant role announcement) do NOT count.
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
	if chunk.Delta.Content != "" || chunk.Delta.ReasoningContent != "" {
		return true
	}
	if len(chunk.Delta.ToolCalls) > 0 {
		return true
	}
	if chunk.Delta.AudioDelta != nil &&
		(chunk.Delta.AudioDelta.Data != "" || chunk.Delta.AudioDelta.Transcript != "") {
		return true
	}
	return false
}

// ChatSemanticDelta reports whether payload is a valid OpenAI delta frame
// with at least one choice but no semantic output. It deliberately excludes
// usage, keepalive, malformed payloads, and empty/missing choices so those
// protocol-control frames cannot trigger early-empty failover.
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
	return err == nil && chunk != nil && chunk.Type == ir.ChunkTypeDelta && chunk.Delta != nil && !ChatChunkHasOutput(payload)
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
			ToolCalls        []any           `json:"tool_calls"`
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
		if choice.Message.ReasoningContent != "" || len(choice.Message.ToolCalls) > 0 {
			return false
		}
		if ChatContentHasOutput(choice.Message.Content) {
			return false
		}
	}
	return true
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
		Type      string `json:"type"`
		Text      string `json:"text"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
		ID        string `json:"id"`
		Name      string `json:"name"`
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
			if block.Thinking != "" || block.Signature != "" {
				return false
			}
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
		Type      string `json:"type"`
		Text      string `json:"text"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
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
			if block.Thinking != "" || block.Signature != "" {
				return false
			}
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
