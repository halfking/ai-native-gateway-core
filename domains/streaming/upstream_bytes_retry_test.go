package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// Exercise the real optional-parameter retry: both the rejected first body and
// the accepted second body remain reviewable, in their original HTTP order.
func TestUpstreamBodiesRetryAttempts(t *testing.T) {
	fake := &upstreamBytesFixture{protocol: "responses", rejectedField: "service_tier"}
	server := httptest.NewServer(fake)
	defer server.Close()
	candidate := provider.Candidate{ProviderID: 71, CredentialID: 72, BaseURL: server.URL, Protocol: "openai-responses", CatalogCode: "vapeur", RawModel: "m", OfferRawModel: "m", APIKey: "test-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed", SupportsNativeResponses: true, SupportsNativeResponsesKnown: true, SupportsNativeResponsesStream: true}
	input := strings.TrimSuffix(upstreamBytesChat, "}") + `,"service_tier":"priority","stream":false}`
	rec := upstreamBytesRequest(t, candidate, "chat", input)
	attempts := fake.captured()
	if rec.Code != 200 || len(attempts) != 2 {
		t.Fatalf("retry status=%d attempts=%s client=%s", rec.Code, upstreamBytesAttemptsDescription(attempts), rec.Body.String())
	}
	for i, attempt := range attempts {
		if attempt.Path != "/v1/responses" {
			t.Fatalf("attempt %d path=%s", i, attempt.Path)
		}
		if err := upstreamBytesSemantics(attempt.Body, "responses"); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(attempt.Body, &fields)
		_, present := fields["service_tier"]
		if present != (i == 0) || ((attempt.Rejection != "") != (i == 0)) {
			t.Fatalf("retry premise/order invalid: %s", upstreamBytesAttemptsDescription(attempts))
		}
	}
	if bytes.Equal(attempts[0].Body, attempts[1].Body) {
		t.Fatal("retry never changed rejected body")
	}
	if err := upstreamBytesClientTerminal(rec, "chat", false); err != nil {
		t.Fatal(err)
	}
	for i, attempt := range attempts {
		path := filepath.Join("testdata", "upstream_bodies", fmt.Sprintf("chat_to_responses_retry.attempt-%02d.json", i+1))
		if err := upstreamBytesGolden(path, attempt.Body, os.Getenv("UPDATE_UPSTREAM_BODIES") == "1"); err != nil {
			t.Fatal(err)
		}
	}
}
