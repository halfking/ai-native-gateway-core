// Package response provides response interception hooks for LLM gateway.
//
// ResponseInterceptor allows processing LLM responses before they are
// forwarded to clients, enabling features like automatic handoff,
// goal-mode continuation, and task completion detection.
package response

import (
	"context"
	"encoding/json"
)

// InterceptRequest contains the context for intercepting a non-streaming response.
type InterceptRequest struct {
	SessionID      string
	RequestID      string
	TenantID       string
	ClientModel    string
	ResponseBody   []byte
	TokensUsed     int
	ContextWindow  int
	MessageCount   int
	FinishReason   string
	IsStreaming    bool
	FollowUpAction string

	// ClientSignalAllowed/HandoffSignalAllowed are request-local capability
	// gates populated at the HTTP boundary. Hooks use them to decide whether
	// to return a client-driven signal or retain the legacy InjectFollowUp
	// behavior; the goal domain deliberately does not depend on streaming.
	ClientSignalAllowed  bool
	HandoffSignalAllowed bool
	SubAgentsTotal       int
	SubAgentsCompleted   int
	SubAgentsPending     int
}

// InterceptResult contains the outcome of response interception.
type InterceptResult struct {
	ShouldBlock    bool
	ModifiedBody   []byte
	InjectFollowUp []byte
	Action         string
	Metadata       map[string]interface{}

	// ClientSignalKind is an opt-in control instruction for a gateway-aware
	// client (currently "gw-continue" or "gw-handoff"). The handler emits it
	// only after validating the request's corresponding capability. A non-empty
	// signal takes precedence over InjectFollowUp for that response.
	ClientSignalKind     string
	ClientSignalPayload  []byte
	ClientSignalAttempts int
}

// StreamMeta contains metadata for stream chunk interception.
//
// ResponseBody and FinishReason are populated at stream end (InterceptStreamEnd)
// by reassembling the streamed chunks into a single non-streaming-style
// response body. This lets stream-end interceptors run the same completion
// detection and audit logic as the non-streaming path. Empty when the caller
// has not reassembled the body.
type StreamMeta struct {
	SessionID      string
	RequestID      string
	TenantID       string
	ClientModel    string
	ContextWindow  int
	MessageCount   int
	TokensUsed     int
	ChunkIndex     int
	ResponseBody   []byte
	FinishReason   string
	FollowUpAction string

	// Request-local capability gates propagated from the HTTP handler.
	ClientSignalAllowed  bool
	HandoffSignalAllowed bool
	SubAgentsTotal       int
	SubAgentsCompleted   int
	SubAgentsPending     int
}

// ChunkResult contains the outcome of stream chunk interception.
type ChunkResult struct {
	ShouldBlock   bool
	ModifiedChunk []byte
	InjectAfter   []byte
}

// EndResult contains the outcome of stream end interception.
type EndResult struct {
	InjectFollowUp []byte
	Action         string
	Metadata       map[string]interface{}

	// ClientSignalKind is an opt-in control instruction for a gateway-aware
	// client (currently "gw-continue" or "gw-handoff").
	ClientSignalKind     string
	ClientSignalPayload  []byte
	ClientSignalAttempts int
}

// ResponseInterceptor is the interface for response interception hooks.
type ResponseInterceptor interface {
	InterceptNonStream(ctx context.Context, req *InterceptRequest) (*InterceptResult, error)
	InterceptStreamChunk(ctx context.Context, chunk []byte, meta *StreamMeta) (*ChunkResult, error)
	InterceptStreamEnd(ctx context.Context, meta *StreamMeta) (*EndResult, error)
}

// ExtractLastAssistantMessage extracts the last assistant message content
func ExtractLastAssistantMessage(body []byte) (string, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) > 0 && resp.Choices[0].Message.Role == "assistant" {
		return resp.Choices[0].Message.Content, nil
	}
	return "", nil
}
