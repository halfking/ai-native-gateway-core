
package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestFactory_Accessors(t *testing.T) {
	f := NewTransportFactory()
	if f.IR() == nil {
		t.Fatal("IR() should not be nil")
	}
	if f.Legacy() == nil {
		t.Fatal("Legacy() should not be nil")
	}
	if f.IR().Implementation() != "ir" {
		t.Fatalf("IR().Implementation() = %s, want ir", f.IR().Implementation())
	}
	if f.Legacy().Implementation() != "legacy" {
		t.Fatalf("Legacy().Implementation() = %s, want legacy", f.Legacy().Implementation())
	}
}

// mockSSEUpstreamResponse 构造一个模拟上游 SSE 流的 http.Response。
func mockSSEUpstreamResponse(t *testing.T, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: req,
	}
	return resp
}

func TestLegacyTransport_ConvertStream_OpenAIPassthrough(t *testing.T) {
	tr := NewLegacyTransport()
	w := httptest.NewRecorder()
	resp := mockSSEUpstreamResponse(t, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")

	env := domain.NewEnvelopeBuilder("r1").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "openai-chat",
			ClientModel:      "gpt-4o",
			OutboundModel:    "gpt-4o",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "choices") {
		t.Fatalf("stream output missing choices: %q", body)
	}
}

func TestIRTransport_ConvertStream_OpenAIToOpenAI(t *testing.T) {
	tr := NewIRTransport()
	w := httptest.NewRecorder()
	// OpenAI SSE 流
	sse := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	resp := mockSSEUpstreamResponse(t, sse)

	env := domain.NewEnvelopeBuilder("r1").
		WithTransport(&domain.TransportContext{
			W:                w,
			IsStream:         true,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "openai-chat",
			ClientModel:      "gpt-4o",
			OutboundModel:    "gpt-4o",
		}).
		Build()

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}

	body := w.Body.String()
	if len(body) == 0 {
		t.Fatal("stream output empty")
	}
}

func TestIRTransport_ConvertStream_NilInput(t *testing.T) {
	tr := NewIRTransport()
	if err := tr.ConvertStream(context.Background(), nil, nil); err == nil {
		t.Fatal("ConvertStream(nil) should error")
	}
}

func TestLegacyTransport_ConvertStream_NilInput(t *testing.T) {
	tr := NewLegacyTransport()
	if err := tr.ConvertStream(context.Background(), nil, nil); err == nil {
		t.Fatal("ConvertStream(nil) should error")
	}
}

func TestLegacyTransport_ConvertStream_Unsupported(t *testing.T) {
	tr := NewLegacyTransport()
	w := httptest.NewRecorder()
	resp := mockSSEUpstreamResponse(t, "data: {}\n\n")

	env := domain.NewEnvelopeBuilder("r1").
		WithTransport(&domain.TransportContext{
			W:                w,
			ClientProtocol:   "openai-chat",
			UpstreamProtocol: "openai-chat",
		}).
		Build()

	// anthropic client → openai upstream stream is unsupported in legacy
	env.Transport.ClientProtocol = "anthropic-messages"
	env.Transport.UpstreamProtocol = "openai-chat"
	if err := tr.ConvertStream(context.Background(), env, resp); err == nil {
		t.Fatal("ConvertStream with unsupported stream conversion should error")
	}
}

func TestSetActiveImplementation(t *testing.T) {
	SetActiveImplementation("ir", true)
	SetActiveImplementation("legacy", false)
	// 只要不 panic 即可
}

// 确保 bytes import 被使用（未来扩展可能用）
var _ = bytes.NewReader
