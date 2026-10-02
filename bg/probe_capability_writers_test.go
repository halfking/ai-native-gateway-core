package bg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNodeProbeDirectPersistsNativeResponsesVerdict(t *testing.T) {
	tests := []struct {
		name             string
		responsesCode    int
		responsesBody    string
		chatCode         int
		wantSupported    bool
		wantChatFallback bool
	}{
		{
			name:          "native success",
			responsesCode: http.StatusOK,
			responsesBody: `{"id":"resp_1","object":"response","status":"completed","output":[]}`,
			wantSupported: true,
		},
		{
			name:             "unsupported remains negative after chat succeeds",
			responsesCode:    http.StatusBadRequest,
			responsesBody:    vapeurClaudeResponses400,
			chatCode:         http.StatusOK,
			wantSupported:    false,
			wantChatFallback: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/responses":
					w.WriteHeader(tt.responsesCode)
					_, _ = w.Write([]byte(tt.responsesBody))
				case "/v1/chat/completions":
					w.WriteHeader(tt.chatCode)
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
				default:
					t.Errorf("unexpected probe path %q", r.URL.Path)
				}
			}))
			t.Cleanup(srv.Close)

			sink := &recordingResponsesCapabilitySink{}
			worker := NewNodeProbeWorker(nil, nil, nil, "", srv.URL, nil)
			worker.probeClient = srv.Client()
			worker.SetResponsesCapabilitySink(sink)
			worker.resolveDirectTargetFn = func(context.Context, int, string) (string, string, string, string, int, error) {
				return "sk-test", "claude-sonnet-5", srv.URL, "openai-responses", 36, nil
			}

			result := worker.probeDirect(t.Context(), 126, "claude-sonnet-5")
			if result.supportsResponses == nil || *result.supportsResponses != tt.wantSupported {
				t.Fatalf("direct capability = %v, want %v", result.supportsResponses, tt.wantSupported)
			}
			if result.chatFallback != tt.wantChatFallback {
				t.Fatalf("chatFallback = %v, want %v", result.chatFallback, tt.wantChatFallback)
			}
			if len(sink.records) != 1 || sink.records[0].credentialID != 126 || sink.records[0].model != "claude-sonnet-5" || sink.records[0].supported != tt.wantSupported {
				t.Fatalf("persisted capability verdicts = %+v", sink.records)
			}
		})
	}
}

func TestModelProbePersistsFallbackCapabilityVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(vapeurClaudeResponses400))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
		default:
			t.Errorf("unexpected probe path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	target := probeTarget{CredentialID: 128, RawModel: "claude-sonnet-5", BaseURL: srv.URL, Protocol: "openai-responses", APIKey: "sk-test"}
	result := probeWithRetry(t.Context(), probeDescriptorFor(target.Protocol), target, ProbeModeResponses)
	sink := &recordingResponsesCapabilitySink{}
	runner := NewModelProbeRunner(nil, nil)
	runner.SetResponsesCapabilitySink(sink)
	runner.persistResponsesCapability(t.Context(), target, result)

	if len(sink.records) != 1 || sink.records[0].credentialID != target.CredentialID || sink.records[0].model != target.RawModel || sink.records[0].supported {
		t.Fatalf("persisted capability verdicts = %+v, want false for native unsupported response", sink.records)
	}
}
