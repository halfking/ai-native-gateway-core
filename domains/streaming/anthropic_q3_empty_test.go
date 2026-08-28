package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const emptyAnthropicMessageStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_empty\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestStreamAnthropicSSEToOpenAIEmptyMessageIsRetryable(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(emptyAnthropicMessageStream)), Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil)}
	rec := httptest.NewRecorder()

	out := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp, "claude", "claude", "req-q3-empty", nil, nil)

	if !out.Interrupted || out.Kind != errorsx.KindEmptyResponse || !out.Resumable {
		t.Fatalf("outcome = %+v, want retryable empty response", out)
	}
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("empty response must not emit OpenAI success terminator: %q", rec.Body.String())
	}
}

func TestStreamAnthropicSSEToResponsesEmptyMessageIsRetryable(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(emptyAnthropicMessageStream)), Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil)}
	rec := httptest.NewRecorder()

	out := StreamAnthropicSSEToResponses(context.Background(), rec, resp, "claude", "claude", "req-q3-responses-empty", nil, nil)

	if !out.Interrupted || out.Kind != errorsx.KindEmptyResponse || !out.Resumable {
		t.Fatalf("outcome = %+v, want retryable empty response", out)
	}
	if strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("empty response must not emit Responses success completion: %q", rec.Body.String())
	}
}
