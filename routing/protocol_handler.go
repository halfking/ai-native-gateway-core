package routing

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// ProtocolHandler encapsulates all protocol-specific behavior for an
// executor run. Each method corresponds to one decision point the
// common executor MUST make (build req, parse resp, extract usage,
// detect model mismatch). Implementing it as a separate type per
// protocol prevents OpenAI-shaped assumptions from leaking into
// Anthropic code paths (and vice versa).
type ProtocolHandler interface {
	// BuildRequest assembles the upstream HTTP request from the body
	// bytes the gateway is forwarding. It MUST set auth headers
	// appropriate to the protocol (OpenAI: Bearer, Anthropic: x-api-key
	// + anthropic-version). It MUST NOT mutate body in a way that
	// breaks protocol semantics (e.g. inject OpenAI stream_options
	// into an Anthropic request).
	BuildRequest(cand provider.Candidate, body []byte, isStream bool) (*http.Request, error)

	// WriteNonStreamResponse writes a complete upstream response to
	// the client. It is called only when the request was non-stream
	// and the upstream responded 2xx with a parseable body.
	//
	// qualityFixMode is the per-provider tool_call quality mode loaded
	// from cand.QualityFixMode (017_quality_fix_mode.sql). Empty
	// string means "off" — implementations MUST skip the quality
	// post-processor in that case. The hook is part of the
	// interface signature (rather than a separate Executor field)
	// so that the Q3 Anthropic → OpenAI conversion path can apply
	// the same OpenAI-shaped quality check that ChatExecutor runs.
	WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string) error

	// StreamResponse reads an upstream streaming response and writes
	// it to the client. Returns StreamOutcome describing whether
	// the stream completed cleanly, was interrupted, etc.
	StreamResponse(w http.ResponseWriter, resp *http.Response) StreamOutcome

	// ExtractUsage pulls token counts out of the upstream response.
	// For OpenAI, this is a single body read. For Anthropic, the
	// handler MUST accumulate across message_start (input_tokens)
	// and message_delta (output_tokens) events.
	ExtractUsage(resp *http.Response, body []byte) (inputTokens, outputTokens *int)

	// CheckSoftMismatch returns true if the upstream model name
	// doesn't match the requested one. Used to detect silent
	// fallbacks like minimax returning M3 for unknown model names.
	// MUST NOT depend on HTTP status code (upstream may return 200).
	CheckSoftMismatch(reqModel, respModel string) (mismatched bool, reason string)
}
