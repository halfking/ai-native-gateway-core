package transformation

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func newStreamEnvelope(client, upstream string, w http.ResponseWriter, model string) *domain.RequestEnvelope {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return &domain.RequestEnvelope{
		RequestID: "test-stream",
		Transport: &domain.TransportContext{
			W:                w,
			R:                req,
			ClientProtocol:   client,
			UpstreamProtocol: upstream,
			ClientModel:      model,
			OutboundModel:    model,
			BodyBytes:        []byte(openaiBody),
		},
	}
}

func newUpstreamResponse(body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
}

func TestLegacyTransport_ConvertStream_OpenAIPassthroughCopy(t *testing.T) {
	tr := NewLegacyTransport()
	rec := httptest.NewRecorder()
	env := newStreamEnvelope("openai-chat", "openai-chat", rec, "gpt-4o")
	resp := newUpstreamResponse(io.NopCloser(strings.NewReader("data: hello\n\ndata: world\n\n")))

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream: %v", err)
	}
	if got := rec.Body.String(); got != "data: hello\n\ndata: world\n\n" {
		t.Fatalf("passthrough body=%q, want verbatim copy", got)
	}
}

// TestLegacyTransport_ConvertStream_AnthropicPassthrough proves the
// StreamWriter satisfies http.Flusher and is accepted by
// StreamAnthropicPassthrough unchanged, while still forwarding bytes.
func TestLegacyTransport_ConvertStream_AnthropicPassthrough(t *testing.T) {
	tr := NewLegacyTransport()
	rec := httptest.NewRecorder()
	env := newStreamEnvelope("anthropic-messages", "anthropic-messages", rec, "claude-3-5-sonnet")
	body := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-3-5-sonnet\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	resp := newUpstreamResponse(io.NopCloser(strings.NewReader(body)))

	if err := tr.ConvertStream(context.Background(), env, resp); err != nil {
		t.Fatalf("ConvertStream Q4: %v", err)
	}
	got := rec.Body.String()
	if !strings.Contains(got, "message_start") || !strings.Contains(got, "claude-3-5-sonnet") {
		t.Fatalf("Q4 passthrough body=%q, missing expected frames", got)
	}
}

// blockingBody yields its data once, then blocks every subsequent Read until
// Close is called. Used to exercise context-timeout cancellation of a stalled
// upstream read.
type blockingBody struct {
	data   []byte
	mu     sync.Mutex
	sent   bool
	closed chan struct{}
}

func newBlockingBody(data []byte) *blockingBody {
	return &blockingBody{data: data, closed: make(chan struct{})}
}

func (b *blockingBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	sent := b.sent
	if !sent {
		b.sent = true
		b.mu.Unlock()
		return copy(p, b.data), nil
	}
	b.mu.Unlock()
	<-b.closed
	return 0, errors.New("body closed")
}

func (b *blockingBody) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func TestLegacyTransport_ConvertStream_Timeout(t *testing.T) {
	tr := NewLegacyTransport()
	rec := httptest.NewRecorder()
	env := newStreamEnvelope("openai-chat", "openai-chat", rec, "gpt-4o")
	resp := newUpstreamResponse(newBlockingBody([]byte("data: partial\n\n")))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := tr.ConvertStream(ctx, env, resp)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ConvertStream timeout: err=%v, want context.DeadlineExceeded", err)
	}
}

// errWriteRW is an http.ResponseWriter whose Write always fails, simulating a
// client disconnect mid-stream.
type errWriteRW struct {
	*httptest.ResponseRecorder
}

func (e *errWriteRW) Write(p []byte) (int, error) {
	return 0, errors.New("injected write failure")
}

func TestLegacyTransport_ConvertStream_WriteError(t *testing.T) {
	tr := NewLegacyTransport()
	w := &errWriteRW{httptest.NewRecorder()}
	env := newStreamEnvelope("openai-chat", "openai-chat", w, "gpt-4o")
	resp := newUpstreamResponse(io.NopCloser(strings.NewReader("data: hello\n\n")))

	err := tr.ConvertStream(context.Background(), env, resp)
	if err == nil {
		t.Fatal("ConvertStream should surface the client write error")
	}
}
