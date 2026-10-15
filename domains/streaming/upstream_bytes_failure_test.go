package streaming

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func upstreamBytesVerifiedResponses(url string) provider.Candidate {
	return provider.Candidate{ProviderID: 11, CredentialID: 22, BaseURL: url, Protocol: "openai-responses", CatalogCode: "vapeur", RawModel: "m", OfferRawModel: "m", APIKey: "test-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed", SupportsNativeResponses: true, SupportsNativeResponsesKnown: true, SupportsNativeResponsesStream: true}
}

func TestUpstreamBodiesResponsesBridgeFailureTerminals(t *testing.T) {
	for _, client := range []string{"chat", "messages"} {
		for _, tc := range []struct {
			name, terminal string
			incomplete     bool
		}{
			{"failed", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"failure probe"}}}`, false},
			{"typed_error", `{"type":"error","code":"server_error","message":"failure probe"}`, false},
			{"completed_name_failed_status", `{"type":"response.completed","response":{"status":"failed","error":{"code":"server_error","message":"failure probe"}}}`, false},
			{"incomplete", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, true},
			{"content_filter", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, true},
			{"unknown_incomplete", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"unrecognized"}}}`, false},
			{"truncated", "", false},
		} {
			t.Run(client+"_"+tc.name, func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					raw, _ := io.ReadAll(r.Body)
					if err := upstreamBytesWireError("responses", r.URL.Path, raw); err != "" {
						t.Errorf("wrong request wire: %s", err)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
					if tc.terminal != "" {
						_, _ = io.WriteString(w, "data: "+tc.terminal+"\n\n")
					}
				}))
				defer upstream.Close()
				input := upstreamBytesChat
				if client == "messages" {
					input = upstreamBytesMessages
				}
				input = strings.TrimSuffix(input, "}") + `,"stream":true}`
				rec, entries := upstreamBytesFailureRequest(t, upstreamBytesVerifiedResponses(upstream.URL), client, input)
				wire := rec.Body.String()
				if calls != 1 || !strings.Contains(wire, "partial") {
					t.Fatalf("premise missing calls=%d body=%s", calls, wire)
				}
				if tc.incomplete {
					reason := `"finish_reason":"length"`
					terminal := "data: [DONE]"
					if client == "messages" {
						reason = `"stop_reason":"max_tokens"`
						terminal = "event: message_stop"
					}
					if tc.name == "content_filter" {
						reason = `"finish_reason":"content_filter"`
						if client == "messages" {
							reason = `"stop_reason":"refusal"`
						}
					}
					if !strings.Contains(wire, reason) || strings.Count(wire, terminal) != 1 {
						t.Fatalf("incomplete reason/terminal lost: %s", wire)
					}
				} else {
					if len(entries) == 0 {
						t.Fatal("missing failure audit premise")
					}
					for _, entry := range entries {
						if entry.Success {
							t.Fatalf("failed upstream recorded success: %+v", entry)
						}
					}
					if !strings.Contains(wire, `"error"`) || strings.Contains(wire, `"finish_reason":"stop"`) || strings.Contains(wire, `"stop_reason":"end_turn"`) || strings.Contains(wire, "event: message_stop") {
						t.Fatalf("failed upstream became successful stream: %s", wire)
					}
				}
			})
		}
	}
}

func TestUpstreamBodiesMessagesThinkingIntent(t *testing.T) {
	for _, tc := range []struct {
		mode, effort string
		budget       int
	}{{"enabled", "high", 4096}, {"adaptive", "medium", 0}, {"disabled", "", 0}, {"enabled_explicit", "low", 4096}} {
		for _, streamed := range []bool{false, true} {
			t.Run(tc.mode+map[bool]string{false: "_json", true: "_stream"}[streamed], func(t *testing.T) {
				fake := &upstreamBytesFixture{protocol: "responses"}
				server := httptest.NewServer(fake)
				defer server.Close()
				fields := map[string]any{"model": "m", "max_tokens": 128, "messages": []map[string]any{{"role": "user", "content": "hi"}}, "thinking": map[string]any{"type": strings.TrimSuffix(tc.mode, "_explicit"), "budget_tokens": tc.budget}, "stream": streamed}
				if strings.HasSuffix(tc.mode, "_explicit") {
					fields["reasoning_effort"] = tc.effort
				}
				input, _ := json.Marshal(fields)
				rec := upstreamBytesRequest(t, upstreamBytesVerifiedResponses(server.URL), "messages", string(input))
				attempts := fake.captured()
				if rec.Code != 200 || len(attempts) != 1 {
					t.Fatalf("thinking exchange failed: %d %s", rec.Code, rec.Body.String())
				}
				var wire struct {
					Reasoning struct{ Effort string }
					Thinking  json.RawMessage
				}
				_ = json.Unmarshal(attempts[0].Body, &wire)
				if wire.Reasoning.Effort != tc.effort || len(wire.Thinking) > 0 {
					t.Fatalf("thinking intent changed: want %s got %s", tc.effort, attempts[0].Body)
				}
				if err := upstreamBytesClientTerminal(rec, "messages", streamed); err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("messages_thinking_%s_%s.attempt-01.json", tc.mode, map[bool]string{false: "json", true: "stream"}[streamed])
				if err := upstreamBytesGolden(filepath.Join("testdata", "upstream_bodies", name), attempts[0].Body, os.Getenv("UPDATE_UPSTREAM_BODIES") == "1"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func upstreamBytesFailureRequest(t *testing.T, candidate provider.Candidate, client, body string) (*httptest.ResponseRecorder, []*telemetry.RequestLogEntry) {
	t.Helper()
	handler := upstreamBytesHandler(t, candidate, client)
	var chat *ChatHandler
	switch h := handler.(type) {
	case *ChatHandler:
		chat = h
	case *MessagesHandler:
		chat = h.chatHandler
	}
	tc := telemetry.NewClient()
	tc.SetRequestLogSink(nativeOutputAuditSink{})
	t.Cleanup(tc.Stop)
	chat.SetTelemetry(tc)
	var entries []*telemetry.RequestLogEntry
	chat.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { copy := *entry; entries = append(entries, &copy) })
	req := httptest.NewRequest(http.MethodPost, upstreamBytesPath(client), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-golden")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, entries
}
