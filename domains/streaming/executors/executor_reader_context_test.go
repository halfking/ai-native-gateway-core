package executors

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func assertDetachedReaderContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if ctx == nil {
		t.Fatal("stream reader received nil context")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("stream reader context already canceled: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("detached stream reader context has no lifetime deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= time.Hour || remaining > detachedStreamMaxLifetime {
		t.Fatalf("reader context deadline remaining = %v, want within (1h, %v]", remaining, detachedStreamMaxLifetime)
	}
}

func canceledSessionRequest(path string) *http.Request {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
	cancel()
	return req
}

func TestExecuteOpenAI_StreamReadersReceiveDetachedUpstreamContext(t *testing.T) {
	body := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	responsesBody := []byte(`{"model":"gpt-test","input":"hi","stream":true}`)
	tests := []struct {
		name           string
		clientProtocol string
		native         bool
		install        func(*Executor, func(context.Context, *http.Response))
	}{
		{
			name:           "chat",
			clientProtocol: "openai-completions",
			install: func(e *Executor, check func(context.Context, *http.Response)) {
				e.StreamChat = func(ctx context.Context, _ http.ResponseWriter, resp *http.Response, _, _, _ string, _ NormalizerFunc, _ *audit.StreamCapture, _ bool) StreamOutcome {
					check(ctx, resp)
					return StreamOutcome{ChunkCount: 1}
				}
			},
		},
		{
			name:           "chat-to-anthropic",
			clientProtocol: "anthropic-messages",
			install: func(e *Executor, check func(context.Context, *http.Response)) {
				e.OpenAIToAnthropicStream = func(ctx context.Context, _ http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any, _ int, _ bool) StreamOutcome {
					check(ctx, resp)
					return StreamOutcome{ChunkCount: 1}
				}
			},
		},
		{
			name:           "chat-to-responses",
			clientProtocol: "openai-responses",
			install: func(e *Executor, check func(context.Context, *http.Response)) {
				e.OpenAIToResponsesStream = func(ctx context.Context, _ http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any, _ bool) StreamOutcome {
					check(ctx, resp)
					return StreamOutcome{ChunkCount: 1}
				}
			},
		},
		{
			name:           "native-responses",
			clientProtocol: "openai-responses",
			native:         true,
			install: func(e *Executor, check func(context.Context, *http.Response)) {
				e.NativeResponsesStream = func(ctx context.Context, _ http.ResponseWriter, resp *http.Response, _ string, _ *audit.StreamCapture, _ *atomic.Bool) StreamOutcome {
					check(ctx, resp)
					return StreamOutcome{ChunkCount: 1}
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			exec := newOverloadTestExecutor()
			called := false
			check := func(ctx context.Context, resp *http.Response) {
				called = true
				defer resp.Body.Close()
				assertDetachedReaderContext(t, ctx)
			}
			tc.install(exec, check)

			candidate := overloadTestCandidate(upstream.URL)
			if tc.native {
				candidate = nativeResponsesStreamCandidate(upstream.URL, true)
			}
			params := &ExecParams{
				W:                          httptest.NewRecorder(),
				R:                          canceledSessionRequest("/v1/chat/completions"),
				BodyBytes:                  body,
				ResponsesBodyBytes:         responsesBody,
				IsStream:                   true,
				StreamSurvivesClientCancel: true,
				ClientProtocol:             tc.clientProtocol,
				ClientModel:                "gpt-test",
				OutboundModel:              "gpt-test",
			}
			result, err := exec.executeOpenAI(params, candidate, 0, time.Now(), nil)
			if err != nil || result == nil {
				t.Fatalf("executeOpenAI() = (%#v, %v)", result, err)
			}
			if !called {
				t.Fatal("stream reader callback was not called")
			}
		})
	}
}

func TestExecuteAnthropic_StreamReadersReceiveDetachedUpstreamContext(t *testing.T) {
	tests := []struct {
		name           string
		clientProtocol string
		install        func(*Executor, func(context.Context, *http.Response))
	}{
		{
			name:           "messages-passthrough",
			clientProtocol: "anthropic-messages",
			install: func(e *Executor, check func(context.Context, *http.Response)) {
				e.AnthropicPassthroughStream = func(ctx context.Context, _ http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any) StreamOutcome {
					check(ctx, resp)
					return StreamOutcome{ChunkCount: 1}
				}
			},
		},
		{
			name:           "messages-to-chat",
			clientProtocol: "openai-completions",
			install: func(e *Executor, check func(context.Context, *http.Response)) {
				e.AnthropicToOpenAIStream = func(ctx context.Context, _ http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any) StreamOutcome {
					check(ctx, resp)
					return StreamOutcome{ChunkCount: 1}
				}
			},
		},
		{
			name:           "messages-to-responses",
			clientProtocol: "openai-responses",
			install: func(e *Executor, check func(context.Context, *http.Response)) {
				e.AnthropicToResponsesStream = func(ctx context.Context, _ http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any) StreamOutcome {
					check(ctx, resp)
					return StreamOutcome{ChunkCount: 1}
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			exec := newOverloadTestExecutor()
			called := false
			check := func(ctx context.Context, resp *http.Response) {
				called = true
				defer resp.Body.Close()
				assertDetachedReaderContext(t, ctx)
			}
			tc.install(exec, check)

			candidate := provider.Candidate{
				CredentialID: 11, ProviderID: 22, BaseURL: upstream.URL,
				Protocol: "anthropic-messages", CatalogCode: "anthropic", RawModel: "claude-test", APIKey: "sk-test",
			}
			body := []byte(`{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
			if tc.clientProtocol != "anthropic-messages" {
				body = []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hi"}],"stream":true}`)
			}
			params := &ExecParams{
				W:                          httptest.NewRecorder(),
				R:                          canceledSessionRequest("/v1/messages"),
				BodyBytes:                  body,
				IsStream:                   true,
				StreamSurvivesClientCancel: true,
				ClientProtocol:             tc.clientProtocol,
				ClientModel:                "claude-test",
				OutboundModel:              "claude-test",
			}
			result, err := exec.executeAnthropic(params, candidate, 0, time.Now(), nil)
			if err != nil || result == nil {
				t.Fatalf("executeAnthropic() = (%#v, %v)", result, err)
			}
			if !called {
				t.Fatal("stream reader callback was not called")
			}
		})
	}
}

func TestExecuteOllama_StreamReaderUsesDetachedUpstreamContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"model":"llama3.1","message":{"role":"assistant","content":"hi"},"done":false}`+"\n")
		_, _ = io.WriteString(w, `{"model":"llama3.1","done_reason":"stop","done":true,"prompt_eval_count":1,"eval_count":1}`+"\n")
	}))
	defer upstream.Close()

	body := []byte(`{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	params := &ExecParams{
		W: httptest.NewRecorder(), R: canceledSessionRequest("/v1/chat/completions"),
		BodyBytes: body, IsStream: true, StreamSurvivesClientCancel: true,
		ClientModel: "llama3.1", OutboundModel: "llama3.1", ClientProtocol: "openai-completions",
	}
	candidate := provider.Candidate{
		CredentialID: 7, ProviderID: 42, BaseURL: upstream.URL,
		Protocol: "ollama-native", CatalogCode: "ollama", RawModel: "llama3.1",
	}
	if _, err := newTestExecutor().executeOllama(params, candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("executeOllama() = %v, want the detached reader to finish", err)
	}
	if got := params.W.(*httptest.ResponseRecorder).Body.String(); !strings.Contains(got, `"content":"hi"`) || !strings.Contains(got, "data: [DONE]") {
		t.Fatalf("stream output = %q, want content and clean terminal", got)
	}
}

type streamReadErrorBody struct{}

func (streamReadErrorBody) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }
func (streamReadErrorBody) Close() error             { return nil }

func TestOllamaStreamCanceledReadErrorIsClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp := &http.Response{StatusCode: http.StatusOK, Body: streamReadErrorBody{}, Header: make(http.Header)}
	outcome := (&OllamaExecutor{}).StreamResponse(ctx, httptest.NewRecorder(), resp)
	if !outcome.Interrupted || outcome.Kind != errorsx.KindCanceled || outcome.Reason != "client_cancel" {
		t.Fatalf("outcome = %#v, want client cancellation rather than provider read failure", outcome)
	}
}

type failingStreamResponseWriter struct{ header http.Header }

func (w *failingStreamResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (*failingStreamResponseWriter) WriteHeader(int) {}
func (*failingStreamResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic disconnected client")
}

func TestOllamaStreamClientWriteFailureIsCanceled(t *testing.T) {
	body := `{"model":"llama3.1","message":{"role":"assistant","content":"hi"},"done":false}` + "\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	outcome := (&OllamaExecutor{}).StreamResponse(context.Background(), &failingStreamResponseWriter{}, resp)
	if !outcome.Interrupted || outcome.Kind != errorsx.KindCanceled || outcome.Reason != "client_write_failed" {
		t.Fatalf("outcome = %#v, want canceled client-write failure", outcome)
	}
}
