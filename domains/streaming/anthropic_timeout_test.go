package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
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

type partialThenBlocksBody struct {
	closeUnblocksBody
	readOnce sync.Once
}

func (b *partialThenBlocksBody) Read(p []byte) (int, error) {
	first := false
	b.readOnce.Do(func() { first = true })
	if first {
		copy(p, []byte("data: {"))
		return len("data: {"), nil
	}
	return b.closeUnblocksBody.Read(p)
}

func TestAnthropicPassthroughSilentUpstreamTimesOutAndClosesBody(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")
	body := &closeUnblocksBody{closed: make(chan struct{})}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
		Request:    httptest.NewRequest(http.MethodPost, "http://gateway.test", nil),
	}
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- StreamAnthropicPassthrough(httptest.NewRecorder(), resp, "claude", "claude", "req-timeout", nil, nil)
	}()
	select {
	case outcome := <-done:
		if !outcome.Interrupted || outcome.Reason != "stream_chunk_timeout" || outcome.Kind != errorsx.KindStreamTimeout {
			t.Fatalf("outcome = %+v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Anthropic passthrough timeout did not return")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("response body was not closed on passthrough timeout")
	}
}

func TestAnthropicPassthroughHalfLineTimesOutAndClosesBody(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")
	body := &partialThenBlocksBody{closeUnblocksBody: closeUnblocksBody{closed: make(chan struct{})}}
	resp := &http.Response{StatusCode: http.StatusOK, Body: body, Request: httptest.NewRequest(http.MethodPost, "http://gateway.test", nil)}
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- StreamAnthropicPassthrough(httptest.NewRecorder(), resp, "claude", "claude", "req-half-line", nil, nil)
	}()
	select {
	case outcome := <-done:
		if !outcome.Interrupted || outcome.Reason != "stream_chunk_timeout" {
			t.Fatalf("outcome = %+v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("half-line passthrough timeout did not return")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("response body was not closed on half-line timeout")
	}
}

func TestAnthropicPassthroughRequestCancelReturnsClientCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &closeUnblocksBody{closed: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPost, "http://gateway.test", nil).WithContext(ctx)
	resp := &http.Response{StatusCode: http.StatusOK, Body: body, Request: req}
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- StreamAnthropicPassthrough(httptest.NewRecorder(), resp, "claude", "claude", "req-cancel", nil, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case outcome := <-done:
		if !outcome.Interrupted || outcome.Reason != "client_cancel" || outcome.Kind != errorsx.KindCanceled {
			t.Fatalf("outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("passthrough did not stop after request cancellation")
	}
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
