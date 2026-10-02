package streaming

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/internal/emptyoutcome"
)

// Wave4-D2 (2026-09-22): the empty-outcome semantic table moved verbatim to
// internal/emptyoutcome so the streaming handler, the responses bridge, and
// the executors sub-package (which cannot import this parent package) share
// one judgment. These package-local aliases remain for the streaming call
// sites; new code should call emptyoutcome directly.

type nonStreamResponseFormat = emptyoutcome.Format

const (
	nonStreamResponseUnknown   = emptyoutcome.FormatUnknown
	nonStreamResponseChat      = emptyoutcome.FormatChat
	nonStreamResponseAnthropic = emptyoutcome.FormatAnthropic
	nonStreamResponseResponses = emptyoutcome.FormatResponses
)

func classifyNonStreamUpstreamResponse(body []byte) (nonStreamResponseFormat, bool) {
	return emptyoutcome.ClassifyBody(body)
}

func isEmptyUpstreamChatResponse(body []byte) bool {
	return emptyoutcome.ChatBodyIsEmpty(body)
}

func isEmptyChatChoices(raw json.RawMessage) bool {
	return emptyoutcome.ChatChoicesIsEmpty(raw)
}

func chatContentHasOutput(raw json.RawMessage) bool {
	return emptyoutcome.ChatContentHasOutput(raw)
}

func isEmptyAnthropicContent(raw json.RawMessage) bool {
	return emptyoutcome.AnthropicContentIsEmpty(raw)
}

func isEmptyResponsesOutput(raw json.RawMessage) bool {
	return emptyoutcome.ResponsesOutputIsEmpty(raw)
}
