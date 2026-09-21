package anthropic

import (
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ConvertAnthropicResponseToChat converts an Anthropic Messages
// response (non-stream) into an OpenAI Chat Completions response.
// Used for Q3 (openai client <- anthropic upstream).
//
// R12 候选4: this used to be a second hand-written semantic set and
// hard-failed on real payloads (tool_use.input declared map[string]any, so a
// scalar input unmarshal killed the whole response; thinking stats were
// injected into client-visible bodies as the private `_kxg_meta` field).
// It is now a thin wrapper over the IR (ParseAnthropicResponse →
// SerializeOpenAIResponse) — the same serialization the default IR path uses
// — so the fallback cannot drift from it again.
//
// The empty-response guard is deliberate: a response with zero extractable
// content is an upstream anomaly the executor should fail over from, not an
// empty chat completion silently returned to the client.
func ConvertAnthropicResponseToChat(in []byte, clientModel string) ([]byte, error) {
	parsed, err := ir.ParseAnthropicResponse(in)
	if err != nil {
		return nil, err
	}
	if parsed.Role == "" {
		parsed.Role = "assistant"
	}
	if len(parsed.Content) == 0 && len(parsed.ToolCalls) == 0 && parsed.ReasoningContent == "" {
		// 2026-09-12 audit P1: distinguish "upstream sent nothing" from
		// "upstream sent block types the gateway cannot represent". The
		// caller maps converter errors to errorsx.KindConversion (stage
		// gateway → billing refused, provider NOT demoted); naming the
		// types makes the failure SQL-greppable in request_logs.
		if len(parsed.UnknownBlockTypes) > 0 {
			return nil, fmt.Errorf("unsupported upstream content block types %v from model %s",
				parsed.UnknownBlockTypes, parsed.Model)
		}
		return nil, fmt.Errorf("empty response from model %s", parsed.Model)
	}
	return ir.SerializeOpenAIResponse(parsed, clientModel)
}
