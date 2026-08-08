// Package irconv defines the dependency-free contract shared by protocol
// conversion callers and implementations. Keeping this contract below the
// domain packages avoids import cycles and preserves exact Go method types.
package irconv

import "github.com/kaixuan/llm-gateway-go/internal/ir"

// Converter is the common protocol conversion contract.
type Converter interface {
	ParseOpenAI(body []byte) (*ir.InternalRequest, error)
	ParseAnthropic(body []byte) (*ir.InternalRequest, error)
	ParseResponses(body []byte) (*ir.InternalRequest, error)
	SerializeOpenAI(req *ir.InternalRequest) ([]byte, error)
	SerializeAnthropic(req *ir.InternalRequest) ([]byte, error)
	ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error)
	ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error)
	SerializeOpenAIResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error)
	SerializeAnthropicResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error)
	SerializeResponses(chunk *ir.StreamChunk, itemID string) string
	SerializeResponsesResponse(resp *ir.InternalResponse, clientModel string) ([]byte, error)
}

// ScopedConverter binds conversion and circuit-breaker state to one provider.
type ScopedConverter interface {
	WithProviderScope(providerID int) Converter
}
