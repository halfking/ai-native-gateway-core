package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/paramrefusal"
	"github.com/kaixuan/llm-gateway-go/internal/tooloffer"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// This adapter forwards every operation to production IR. No request conversion
// or stream framing is implemented by the fixture.
type upstreamBytesIR struct{}

func (*upstreamBytesIR) ParseOpenAI(b []byte) (*ir.InternalRequest, error) { return ir.ParseOpenAI(b) }
func (*upstreamBytesIR) ParseAnthropic(b []byte) (*ir.InternalRequest, error) {
	return ir.ParseAnthropic(b)
}
func (*upstreamBytesIR) ParseResponses(b []byte) (*ir.InternalRequest, error) {
	return ir.ParseResponses(b)
}
func (*upstreamBytesIR) SerializeOpenAI(r *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeOpenAI(r)
}
func (*upstreamBytesIR) SerializeAnthropic(r *ir.InternalRequest) ([]byte, error) {
	return ir.SerializeAnthropic(r)
}
func (*upstreamBytesIR) ParseAnthropicResponse(b []byte) (*ir.InternalResponse, error) {
	return ir.ParseAnthropicResponse(b)
}
func (*upstreamBytesIR) ParseOpenAIResponse(b []byte) (*ir.InternalResponse, error) {
	return ir.ParseOpenAIResponse(b)
}
func (*upstreamBytesIR) SerializeOpenAIResponse(r *ir.InternalResponse, m string) ([]byte, error) {
	return ir.SerializeOpenAIResponse(r, m)
}
func (*upstreamBytesIR) SerializeAnthropicResponse(r *ir.InternalResponse, m string) ([]byte, error) {
	return ir.SerializeAnthropicResponse(r, m)
}
func (*upstreamBytesIR) SerializeResponses(r *ir.StreamChunk, id string) string {
	return r.SerializeResponses(id)
}
func (*upstreamBytesIR) SerializeResponsesResponse(r *ir.InternalResponse, m string) ([]byte, error) {
	return ir.SerializeResponsesResponse(r, m)
}

func upstreamBytesHandler(t *testing.T, candidate provider.Candidate, client string) http.Handler {
	t.Helper()
	limiter := credential.NewLimiter()
	t.Cleanup(limiter.Stop)
	norm := NewNormalizer()
	e := executors.NewExecutor(executors.NewRouter(executors.NewStickyCache(), limiter), credential.NewManager(), limiter, pool.NewPoolManager(nil), nil, norm.NormalizeChunk,
		func(ctx context.Context, w http.ResponseWriter, r *http.Response, cm, om, cc string, _ executors.NormalizerFunc, c *audit.StreamCapture, tools bool) executors.StreamOutcome {
			r.Body = NewXMLToolCallCoercingBodyWithNames(r.Body, tooloffer.FromContext(ctx))
			var strip func([]byte) []byte
			if cc == "minimax" {
				strip = NewMiniMaxStreamFieldStripper()
			}
			return StreamChatWithPendingCaptureAndDiagnosticsWithVendor(ctx, w, r, cm, om, norm, c, tools, strip, cc, nil, nil)
		}, nil)
	e.IR = transformation.NewTransportIRConverter(&upstreamBytesIR{})
	e.ParamRefusal = paramrefusal.NewStore()
	e.XMLCoerceNonStreamWithContext = func(ctx context.Context, body []byte) []byte {
		return CoerceXMLToolCallsInChatResponseWithNames(body, tooloffer.FromContext(ctx))
	}
	e.AnthropicPassthroughStream = func(ctx context.Context, w http.ResponseWriter, r *http.Response, cm, om, id string, c *audit.StreamCapture, _ any) executors.StreamOutcome {
		return StreamAnthropicPassthrough(ctx, w, r, cm, om, id, c, nil)
	}
	e.AnthropicToOpenAIStream = func(ctx context.Context, w http.ResponseWriter, r *http.Response, cm, om, id string, c *audit.StreamCapture, _ any) executors.StreamOutcome {
		return StreamAnthropicSSEToOpenAI(ctx, w, r, cm, om, id, c, nil)
	}
	e.OpenAIToAnthropicStream = func(ctx context.Context, w http.ResponseWriter, r *http.Response, cm, om, id string, c *audit.StreamCapture, _ any, estimate int, tools bool) executors.StreamOutcome {
		r.Body = NewXMLToolCallCoercingBodyWithNames(r.Body, tooloffer.FromContext(ctx))
		return StreamOpenAIToAnthropicSSEWithDiagnostics(ctx, w, r, cm, om, id, c, nil, nil, estimate)
	}
	e.AnthropicToResponsesStream = func(ctx context.Context, w http.ResponseWriter, r *http.Response, cm, om, id string, c *audit.StreamCapture, _ any) executors.StreamOutcome {
		return StreamAnthropicSSEToResponses(ctx, w, r, cm, om, id, c, nil)
	}
	e.OpenAIToResponsesStream = func(ctx context.Context, w http.ResponseWriter, r *http.Response, cm, om, id string, c *audit.StreamCapture, _ any, tools bool) executors.StreamOutcome {
		r.Body = NewXMLToolCallCoercingBodyWithNames(r.Body, tooloffer.FromContext(ctx))
		return StreamOpenAIToResponsesSSE(ctx, w, r, cm, om, id, c, nil)
	}
	e.NativeResponsesStream = func(ctx context.Context, w http.ResponseWriter, r *http.Response, id string, c *audit.StreamCapture, visible *atomic.Bool) executors.StreamOutcome {
		return StreamNativeResponsesSSE(ctx, w, r, id, c, visible)
	}
	e.ChatToAnthropic = ConvertChatRequestToAnthropic
	e.AnthropicToOpenAI = ConvertAnthropicBodyToOpenAI
	e.AnthropicToChatResponse = ConvertAnthropicResponseToChat
	e.ChatResponseToAnthropic = ConvertChatResponseToAnthropic
	e.SanitizeAnthropicTools = SanitizeAnthropicToolsInBody
	e.NormalizeOpenAITools = NormalizeToolsInChatBody
	pipeline := e.NewDispatchPipeline(nil)
	pipeline.Start()
	t.Cleanup(pipeline.Stop)
	e.SetDispatchPipeline(pipeline)
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = e
	h.provider = nativeOutputAuditResolver{candidate: candidate}
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-golden"}})
	switch client {
	case "messages":
		return NewMessagesHandler(h)
	case "responses":
		return NewResponsesHandler(h)
	default:
		return h
	}
}

func upstreamBytesRequest(t *testing.T, candidate provider.Candidate, client, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, upstreamBytesPath(client), strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer sk-golden")
	w := httptest.NewRecorder()
	upstreamBytesHandler(t, candidate, client).ServeHTTP(w, r)
	return w
}
