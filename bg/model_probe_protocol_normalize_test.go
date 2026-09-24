package bg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// R60 S3-F4/S3-F5 (2026-09-23 audit): every probe dispatch site resolves the
// raw providers.protocol value, and that column has no CHECK constraint —
// legacy rows carry alias spellings ("anthropic", "openai-response", ...)
// that providercap.Resolve silently maps onto the Bearer-only chat default.
// probeDescriptorFor normalizes before Resolve; these tests pin the routing.

func TestProbeDescriptorFor_NormalizesAliases(t *testing.T) {
	cases := []struct {
		raw        string
		wantProto  string
		wantAuth   string
		wantIsChat bool // ChatProbeEndpoint stays chat-completions
	}{
		{"anthropic", "anthropic-messages", "anthropic", false},
		{"claude", "anthropic-messages", "anthropic", false},
		{"anthropic-message", "anthropic-messages", "anthropic", false},
		{"anthropic-messages", "anthropic-messages", "anthropic", false},
		{"openai-response", "openai-responses", "bearer", true}, // vapeur misspelling
		{"openai-chat", "openai-completions", "bearer", true},
		{"openai", "openai-completions", "bearer", true},
	}
	for _, tc := range cases {
		desc := probeDescriptorFor(tc.raw)
		if desc.Protocol != tc.wantProto {
			t.Errorf("probeDescriptorFor(%q).Protocol = %q, want %q", tc.raw, desc.Protocol, tc.wantProto)
		}
		if desc.AuthStyle != tc.wantAuth {
			t.Errorf("probeDescriptorFor(%q).AuthStyle = %q, want %q", tc.raw, desc.AuthStyle, tc.wantAuth)
		}
		isChat := desc.ChatProbeEndpoint == upstreamurl.EpChatCompletions
		if isChat != tc.wantIsChat {
			t.Errorf("probeDescriptorFor(%q) chat endpoint = %v, want %v", tc.raw, isChat, tc.wantIsChat)
		}
	}
}

// TestProbeDescriptorFor_UnknownStaysRaw: genuinely unrecognized protocols
// keep the raw value (chat-default descriptor), unchanged from pre-R60
// behavior — normalization must not invent semantics.
func TestProbeDescriptorFor_UnknownStaysRaw(t *testing.T) {
	desc := probeDescriptorFor("totally-unknown-proto")
	if desc.Protocol != "totally-unknown-proto" {
		t.Fatalf("Protocol = %q, want raw value preserved", desc.Protocol)
	}
	if desc.AuthStyle != "bearer" {
		t.Fatalf("AuthStyle = %q, want default bearer", desc.AuthStyle)
	}
}

// TestProbeWithRetry_AnthropicMessagesWireShape drives the exact call the
// dispatch sites make for a dirty-protocol ("anthropic") target: after
// normalization the probe must hit POST /v1/messages with x-api-key +
// anthropic-version (not Bearer), and a 2xx classifies as ok. 401 must
// classify as auth (auth_failed evidence) without retrying.
func TestProbeWithRetry_AnthropicMessagesWireShape(t *testing.T) {
	t.Run("2xx ok with anthropic headers", func(t *testing.T) {
		var gotPath, gotAPIKey, gotAuthz, gotVersion, gotModel string
		var gotMaxTokens float64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAPIKey = r.Header.Get("x-api-key")
			gotAuthz = r.Header.Get("Authorization")
			gotVersion = r.Header.Get("anthropic-version")
			var body map[string]any
			raw := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(raw)
			_ = json.Unmarshal(raw, &body)
			gotModel, _ = body["model"].(string)
			gotMaxTokens, _ = body["max_tokens"].(float64)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-fable-5","content":[{"type":"text","text":"."}]}`))
		}))
		t.Cleanup(srv.Close)

		target := probeTarget{
			CredentialID: 1,
			RawModel:     "claude-fable-5",
			BaseURL:      srv.URL,
			Protocol:     "anthropic", // dirty legacy spelling
			APIKey:       "sk-ant-test",
		}
		desc := probeDescriptorFor(target.Protocol)
		if desc.Protocol != "anthropic-messages" {
			t.Fatalf("desc.Protocol = %q, want anthropic-messages", desc.Protocol)
		}
		result := probeWithRetry(context.Background(), desc, target, ProbeModeMessages)

		if gotPath != "/v1/messages" {
			t.Fatalf("probe path = %q, want /v1/messages", gotPath)
		}
		if gotAPIKey != "sk-ant-test" {
			t.Fatalf("x-api-key = %q, want credential key", gotAPIKey)
		}
		if gotAuthz != "" {
			t.Fatalf("Authorization = %q, anthropic probe must not send Bearer", gotAuthz)
		}
		if gotVersion != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", gotVersion)
		}
		if gotModel != "claude-fable-5" {
			t.Fatalf("body model = %q, want probe target model", gotModel)
		}
		if gotMaxTokens != 1 {
			t.Fatalf("max_tokens = %v, want 1", gotMaxTokens)
		}
		if result.status != "ok" || result.category != probeCategoryOK {
			t.Fatalf("status = %q category = %q, want ok", result.status, result.category)
		}
	})

	t.Run("401 auth without retry", func(t *testing.T) {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
		}))
		t.Cleanup(srv.Close)

		target := probeTarget{
			CredentialID: 2,
			RawModel:     "claude-fable-5",
			BaseURL:      srv.URL,
			Protocol:     "anthropic-messages",
			APIKey:       "bad-key",
		}
		desc := probeDescriptorFor(target.Protocol)
		result := probeWithRetry(context.Background(), desc, target, ProbeModeMessages)

		if result.httpStatus != http.StatusUnauthorized {
			t.Fatalf("httpStatus = %d, want 401", result.httpStatus)
		}
		if result.category != probeCategoryProviderError {
			t.Fatalf("category = %q, want provider_error (auth failure, not model unavailability)", result.category)
		}
		if calls != 1 {
			t.Fatalf("probe attempts = %d, want 1 (401 is non-retryable)", calls)
		}
	})
}
