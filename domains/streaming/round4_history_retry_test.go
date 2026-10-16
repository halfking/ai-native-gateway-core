package streaming

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestRound4HistoryReactiveResponsesRetry(t *testing.T) {
	for _, client := range []string{"chat", "responses", "messages"} {
		for _, streamed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_stream_%t", client, streamed), func(t *testing.T) {
				fake := &upstreamBytesFixture{protocol: "responses"}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					wire := "responses"
					chat := strings.HasSuffix(r.URL.Path, "/chat/completions")
					if chat {
						wire = "chat"
					}
					if err == nil {
						err = round4HistoryWireError(body, wire)
					}
					if chat || err != nil {
						rejection := ""
						if err != nil {
							rejection = err.Error()
						}
						fake.mu.Lock()
						fake.attempts = append(fake.attempts, upstreamBytesAttempt{Path: r.URL.Path, Body: body, Rejection: rejection})
						fake.mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						if err != nil {
							_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"tool history pairing rejected"}}`)
						} else {
							_, _ = io.WriteString(w, `{"error":{"message":"This model requires the Responses API. Use the Responses API.","code":4006}}`)
						}
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					fake.ServeHTTP(w, r)
				}))
				defer server.Close()
				// Unknown capability deliberately forces the first Chat attempt.
				candidate := provider.Candidate{ProviderID: 11, CredentialID: 22, BaseURL: server.URL, Protocol: "openai-responses", CatalogCode: "vapeur", RawModel: "m", OfferRawModel: "m", APIKey: "test-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed"}
				input := map[string]string{"chat": round4HistoryChat, "responses": round4HistoryResponses, "messages": round4HistoryMessages}[client]
				input = strings.TrimSuffix(input, "}") + fmt.Sprintf(",\"stream\":%t}", streamed)
				response := upstreamBytesRequest(t, candidate, client, input)
				attempts := fake.captured()
				if response.Code != 200 || len(attempts) != 2 || attempts[0].Path != "/v1/chat/completions" || attempts[1].Path != "/v1/responses" {
					t.Fatalf("mode-retry premise/result failed: HTTP%d attempts=%s client=%s", response.Code, upstreamBytesAttemptsDescription(attempts), response.Body.String())
				}
				for _, attempt := range attempts {
					if attempt.Rejection != "" {
						t.Fatal(attempt.Rejection)
					}
				}
				if err := upstreamBytesClientTerminal(response, client, streamed); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
