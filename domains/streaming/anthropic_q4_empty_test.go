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

func TestStreamAnthropicPassthroughEmptyMessageIsRetryable(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_empty\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	recorder := httptest.NewRecorder()

	outcome := StreamAnthropicPassthrough(context.Background(), recorder, resp, "claude", "claude", "req-empty", nil, nil)

	if !outcome.Interrupted {
		t.Fatal("empty message must be interrupted")
	}
	if outcome.Kind != errorsx.KindEmptyResponse {
		t.Fatalf("empty message kind = %q, want %q", outcome.Kind, errorsx.KindEmptyResponse)
	}
	if !outcome.Resumable {
		t.Fatal("empty message must remain retryable")
	}
}
