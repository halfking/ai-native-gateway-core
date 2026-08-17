package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type closeUnblocksBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *closeUnblocksBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *closeUnblocksBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestAnthropicFirstByteTimeoutClosesBodyAndReturns(t *testing.T) {
	t.Setenv("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", "1")
	body := &closeUnblocksBody{closed: make(chan struct{})}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
		Request:    httptest.NewRequest(http.MethodPost, "http://gateway.test", nil),
	}
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- StreamOpenAIToAnthropicSSE(httptest.NewRecorder(), resp, "model-a", "model-a", "request-timeout", nil, nil)
	}()
	select {
	case outcome := <-done:
		if !outcome.Interrupted || outcome.Reason != "first_byte_timeout" {
			t.Fatalf("outcome = %+v", outcome)
		}
		select {
		case <-body.closed:
		default:
			t.Fatal("response body was not closed on first-byte timeout")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("anthropic first-byte timeout did not return")
	}
}
