package executors

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// QualitySignals is the per-request outcome of the
// tool_call quality post-processor (017_quality_fix_mode.sql). The
// WriteNonStreamResponse implementations write this out so the
// executor can propagate the signals into ExecuteResult, which
// in turn feeds relay/handler.go emitTelemetry →
// request_logs.quality_flags / quality_fix_actions / quality_score.
type QualitySignals struct {
	Flags      []string
	FixActions []byte
	Score      *float64
}

// copyNonStreamResponseHeaders copies end-to-end metadata from an upstream
// response whose body has already been fully read and may have been rewritten.
// Length, encoding, and hop-by-hop headers describe the upstream wire bytes and
// must not survive a body rewrite. Setting the final length explicitly also
// keeps responses correct when a middleware ResponseWriter suppresses the
// net/http server's automatic Content-Length inference.
func copyNonStreamResponseHeaders(dst, src http.Header, bodyLength int) {
	for k, vs := range src {
		if strings.EqualFold(k, "Content-Length") ||
			strings.EqualFold(k, "Content-Encoding") ||
			strings.EqualFold(k, "Connection") ||
			strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	dst.Set("Content-Length", strconv.Itoa(bodyLength))
}

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
	//
	// The optional out parameter `qualitySignals` lets the
	// implementation return the post-processor signals (flags,
	// fix-actions, score) so the executor can stash them on
	// ExecuteResult for emitTelemetry. Implementations that do not
	// run a quality hook MUST leave *qualitySignals untouched; the
	// caller treats nil as "no signals".
	WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) ([]byte, error)

	// StreamResponse reads an upstream streaming response and writes
	// it to the client. Returns StreamOutcome describing whether
	// the stream completed cleanly, was interrupted, etc.
	// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
	StreamResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response) StreamOutcome

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
