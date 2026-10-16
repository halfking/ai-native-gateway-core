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

func TestRound4HistoryActualUpstreamMatrix(t *testing.T) {
	for _, client := range []string{"chat", "responses", "messages"} {
		for _, upstream := range []string{"chat", "responses", "messages"} {
			for _, streamed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s_to_%s_stream_%t", client, upstream, streamed), func(t *testing.T) {
					fake := &upstreamBytesFixture{protocol: upstream}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err == nil {
							err = round4HistoryWireError(body, upstream)
						}
						if err != nil {
							fake.mu.Lock()
							fake.attempts = append(fake.attempts, upstreamBytesAttempt{Path: r.URL.Path, Body: body, Rejection: err.Error()})
							fake.mu.Unlock()
							http.Error(w, `{"error":{"type":"invalid_request_error","message":"tool history does not satisfy strict wire pairing"}}`, http.StatusBadRequest)
							return
						}
						r.Body = io.NopCloser(bytes.NewReader(body))
						fake.ServeHTTP(w, r)
					}))
					defer server.Close()
					protocol := map[string]string{"chat": "openai-completions", "responses": "openai-responses", "messages": "anthropic-messages"}[upstream]
					catalog := "openai"
					if upstream == "messages" {
						catalog = "anthropic"
					}
					candidate := provider.Candidate{ProviderID: 11, CredentialID: 22, BaseURL: server.URL, Protocol: protocol, CatalogCode: catalog, RawModel: "m", OfferRawModel: "m", APIKey: "test-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed"}
					if upstream == "responses" {
						candidate.SupportsNativeResponses = true
						candidate.SupportsNativeResponsesKnown = true
						candidate.SupportsNativeResponsesStream = true
					}
					input := map[string]string{"chat": round4HistoryChat, "responses": round4HistoryResponses, "messages": round4HistoryMessages}[client]
					input = strings.TrimSuffix(input, "}") + fmt.Sprintf(",\"stream\":%t}", streamed)
					response := upstreamBytesRequest(t, candidate, client, input)
					attempts := fake.captured()
					if response.Code != 200 || len(attempts) != 1 || attempts[0].Rejection != "" {
						t.Fatalf("real wire exchange failed: HTTP%d attempts=%s client=%s", response.Code, upstreamBytesAttemptsDescription(attempts), response.Body.String())
					}
					if err := upstreamBytesClientTerminal(response, client, streamed); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
