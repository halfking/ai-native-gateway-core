package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/require"
)

type responsesBlockingBody struct {
	first     []byte
	firstRead sync.Once
	closed    chan struct{}
	closeOnce sync.Once
}

func (b *responsesBlockingBody) Read(p []byte) (int, error) {
	first := false
	b.firstRead.Do(func() { first = true })
	if first {
		return copy(p, b.first), nil
	}
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *responsesBlockingBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func isolateResponsesRuntimeConfig(t *testing.T) {
	t.Helper()
	previousStore := streamConfigStore.Load()
	SetConfigStore(nil)
	t.Cleanup(func() { SetConfigStore(previousStore) })
}

func responsesTimeoutResponse(body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
		Request:    httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/stream", nil),
	}
}

func TestResponsesBridges_PreCommitTimeoutRemainsResumable(t *testing.T) {
	isolateResponsesRuntimeConfig(t)
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")

	cases := []struct {
		name       string
		wantReason string
		run        func(http.ResponseWriter, *http.Response) StreamOutcome
	}{
		{
			name:       "anthropic",
			wantReason: "chunk_timeout",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamAnthropicSSEToResponses(context.Background(), w, resp, "claude-test", "claude-test", "req-timeout-anthropic", nil, nil)
			},
		},
		{
			name:       "openai",
			wantReason: "stream_timeout",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamOpenAIToResponsesSSE(context.Background(), w, resp, "gpt-test", "gpt-test", "req-timeout-openai", nil, nil)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &responsesBlockingBody{closed: make(chan struct{})}
			rec := httptest.NewRecorder()
			done := make(chan StreamOutcome, 1)
			go func() { done <- tc.run(rec, responsesTimeoutResponse(body)) }()

			select {
			case out := <-done:
				require.True(t, out.Interrupted)
				require.Equal(t, tc.wantReason, out.Reason)
				require.Equal(t, errorsx.KindStreamTimeout, out.Kind)
				require.True(t, out.Resumable)
				require.Zero(t, out.ChunkCount)
			case <-time.After(2 * time.Second):
				t.Fatal("Responses bridge timeout did not return")
			}
			select {
			case <-body.closed:
			default:
				t.Fatal("Responses bridge did not close the upstream body")
			}
			require.NotContains(t, rec.Body.String(), "response.completed")
		})
	}
}

func TestResponsesBridges_PostCommitTimeoutIsNotResumable(t *testing.T) {
	isolateResponsesRuntimeConfig(t)
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")

	cases := []struct {
		name string
		data string
		run  func(http.ResponseWriter, *http.Response) StreamOutcome
	}{
		{
			name: "anthropic",
			data: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamAnthropicSSEToResponses(context.Background(), w, resp, "claude-test", "claude-test", "req-post-timeout-anthropic", nil, nil)
			},
		},
		{
			name: "openai",
			data: "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamOpenAIToResponsesSSE(context.Background(), w, resp, "gpt-test", "gpt-test", "req-post-timeout-openai", nil, nil)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &responsesBlockingBody{first: []byte(tc.data), closed: make(chan struct{})}
			rec := httptest.NewRecorder()
			done := make(chan StreamOutcome, 1)
			go func() { done <- tc.run(rec, responsesTimeoutResponse(body)) }()

			var out StreamOutcome
			select {
			case out = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Responses bridge post-commit timeout did not return")
			}
			require.True(t, out.Interrupted)
			require.Equal(t, errorsx.KindStreamTimeout, out.Kind)
			require.False(t, out.Resumable)
			require.Greater(t, out.ChunkCount, 0)
			wire := rec.Body.String()
			require.Contains(t, wire, "hello")
			require.Contains(t, wire, "response.completed")
			require.Contains(t, wire, `"status":"incomplete"`)
			select {
			case <-body.closed:
			default:
				t.Fatal("Responses bridge did not close the upstream body")
			}
		})
	}
}

func TestResponsesBridge_PostCommitNetworkFailureIsNotResumable(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(http.ResponseWriter, *http.Response) StreamOutcome
	}{
		{
			name: "anthropic",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamAnthropicSSEToResponses(context.Background(), w, resp, "claude-test", "claude-test", "req-post-network-anthropic", nil, nil)
			},
		},
		{
			name: "openai",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamOpenAIToResponsesSSE(context.Background(), w, resp, "gpt-test", "gpt-test", "req-post-network-openai", nil, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data string
			if tc.name == "anthropic" {
				data = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
			} else {
				data = "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"
			}
			resp := &http.Response{
				Body:    &errorAfterDataReadCloser{data: []byte(data), err: io.ErrUnexpectedEOF},
				Request: httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/stream", nil),
			}
			rec := httptest.NewRecorder()
			out := tc.run(rec, resp)
			require.True(t, out.Interrupted)
			require.Equal(t, "network_error", out.Reason)
			require.Equal(t, errorsx.KindNetwork, out.Kind)
			require.False(t, out.Resumable)
			require.Greater(t, out.ChunkCount, 0)
			require.Contains(t, rec.Body.String(), "hello")
			require.Contains(t, rec.Body.String(), "response.completed")
		})
	}
}

func TestResponsesBridge_NoSemanticNetworkFailureRemainsResumable(t *testing.T) {
	resp := &http.Response{
		Body:    &errorAfterDataReadCloser{data: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"), err: io.ErrUnexpectedEOF},
		Request: httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/stream", nil),
	}
	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(context.Background(), rec, resp, "claude-test", "claude-test", "req-pre-network-anthropic", nil, nil)
	require.True(t, out.Interrupted)
	require.Equal(t, "network_error", out.Reason)
	require.True(t, out.Resumable)
	require.Zero(t, out.ChunkCount)
	require.NotContains(t, rec.Body.String(), "response.completed")
	require.False(t, strings.Contains(rec.Body.String(), "message_start"))
}
