package reconciliation

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestAttemptTransportFailureKeepsUncertainSendsUnassigned(t *testing.T) {
	failed := errors.New("network failure")
	for _, tc := range []struct {
		err     error
		notSent bool
	}{
		{&net.DNSError{Err: "resolution failed"}, true},
		{&net.OpError{Op: "dial", Err: failed}, true},
		{&tls.CertificateVerificationError{Err: failed}, true},
		{&net.OpError{Op: "write", Err: failed}, false},
		{&net.OpError{Op: "read", Err: failed}, false},
		{context.Canceled, false},
	} {
		if got := failedBeforeRequest(tc.err); got != tc.notSent {
			t.Errorf("%v before request = %t, want %t", tc.err, got, tc.notSent)
		}
	}
}

func TestParseUsageEvidenceKeepsExplicitZeroAndMissingNull(t *testing.T) {
	e := ParseUsageEvidence([]byte(`{"id":"resp_1","usage":{"input_tokens":0,"output_tokens":8,"input_tokens_details":{"cached_tokens":3}}}`))
	if e.InputTokens == nil || *e.InputTokens != 0 {
		t.Fatalf("explicit zero lost: %#v", e.InputTokens)
	}
	if e.OutputTokens == nil || *e.OutputTokens != 8 {
		t.Fatalf("output = %#v", e.OutputTokens)
	}
	if e.CacheReadTokens == nil || *e.CacheReadTokens != 3 {
		t.Fatalf("cache read = %#v", e.CacheReadTokens)
	}
	if e.CacheWriteTokens != nil {
		t.Fatalf("missing cache write must stay NULL: %#v", e.CacheWriteTokens)
	}
	if e.ReportedAmount != nil || e.EstimatedAmount != nil {
		t.Fatal("parser must not invent money")
	}
}

func TestRedirectAttemptDoesNotReuseUnverifiedContractDimensions(t *testing.T) {
	newRequest := func(method, endpoint string) *http.Request {
		t.Helper()
		req, err := http.NewRequest(method, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "test-only")
		return req
	}
	const endpoint = "https://openrouter.ai/v1/chat/completions"
	original := newRequest(http.MethodPost, endpoint)
	a := Attempt{ProviderID: 43, CredentialID: 21, Model: "model", RequestID: "request", RateSnapshot: map[string]any{"reference": "test"}}
	target := newRequest(http.MethodPost, endpoint)
	if got := AttributeRedirect(a, original, target); got.ProviderID != 43 || got.CredentialID != 21 || got.Model != "model" || got.RateSnapshot == nil {
		t.Fatalf("unchanged evidence was cleared: %+v", got)
	}
	target.Header.Del("Authorization")
	if got := AttributeRedirect(a, original, target); got.ProviderID != 43 || got.CredentialID != 0 || got.RateSnapshot != nil {
		t.Fatalf("redirect kept the original credential: %+v", got)
	}
	target = newRequest(http.MethodGet, endpoint)
	if got := AttributeRedirect(a, original, target); got.Model != "" || got.RateSnapshot != nil {
		t.Fatalf("method change kept the original model: %+v", got)
	}
	target = newRequest(http.MethodPost, endpoint)
	target.Host = "other.invalid"
	if got := AttributeRedirect(a, original, target); got.ProviderID != 0 || got.CredentialID != 0 || got.Model != "" || got.RateSnapshot != nil || got.RequestID != "request" {
		t.Fatalf("unexpected attribution after Host override: %+v", got)
	}
	if got := ResponseEndpoint(&http.Response{Request: target}); got != "" {
		t.Fatalf("Host override returned an endpoint for reported money: %q", got)
	}
	if got := ResponseEndpoint(&http.Response{Request: original}); got != endpoint {
		t.Fatalf("original response endpoint=%q", got)
	}
}

func TestParseUsageEvidenceDoesNotTurnJSONNullIntoZero(t *testing.T) {
	e := ParseUsageEvidence([]byte(`{"usage":{"prompt_tokens":null,"input_tokens":7,"completion_tokens":null,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":null}}}`), "openai-chat-completions")
	if e.InputTokens == nil || *e.InputTokens != 7 || e.OutputTokens == nil || *e.OutputTokens != 2 {
		t.Fatalf("usage alias fallback = %#v", e)
	}
	if e.CacheReadTokens != nil {
		t.Fatalf("null cache read must remain missing: %#v", e.CacheReadTokens)
	}
}

func TestEstimateAmountUsesDecimalRatesAndSeparatesCache(t *testing.T) {
	u := ParseUsageEvidence([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":10}}}`), "openai-chat-completions")
	amount, currency, ok := EstimateAmount(u, map[string]any{"input": float64(2), "output": float64(4), "cache_read": float64(.2), "currency": "USD", "protocol": "openai-chat-completions"})
	if !ok || amount != "0.000262000000" || currency != "USD" {
		t.Fatalf("estimate = %q %q %v", amount, currency, ok)
	}
}

func TestParseReportedCostIsProviderScopedAndDecimal(t *testing.T) {
	body := []byte(`{"usage":{"cost":0.00000017}}`)
	amount, currency, semantics, ok := ParseReportedCost(body, "https://openrouter.ai/api/v1/chat/completions")
	if !ok || amount != "0.00000017" || currency != "USD" || semantics != "openrouter_usage_cost_usd" {
		t.Fatalf("reported=%q %q %q %v", amount, currency, semantics, ok)
	}
	if _, _, _, ok = ParseReportedCost(body, "https://api.anthropic.com/v1/messages"); ok {
		t.Fatal("generic usage.cost must not become supplier charge")
	}
	if _, _, _, ok = ParseReportedCost([]byte(`{"usage":{"cost":null}}`), "https://openrouter.ai/api/v1/chat/completions"); ok {
		t.Fatal("null amount must remain missing")
	}
}

func TestProviderRequestIDHeaderPrecedesBody(t *testing.T) {
	id := ProviderRequestID(http.Header{"X-Request-Id": []string{"hdr_1"}}, []byte(`{"id":"body_1"}`))
	if id != "hdr_1" {
		t.Fatalf("id=%q", id)
	}
}

func TestRetriesHaveDistinctAttemptIdentityAndNoGuessedCharge(t *testing.T) {
	base := Attempt{TrafficClass: "business", TenantID: "t", RequestID: "r", AttemptNo: 1}
	one := NewAttempt(base)
	base.AttemptNo = 2
	two := NewAttempt(base)
	if one.ID == "" || two.ID == "" || one.ID == two.ID {
		t.Fatalf("attempt IDs are not unique: %q %q", one.ID, two.ID)
	}
	if one.AttemptNo != 1 || two.AttemptNo != 2 {
		t.Fatalf("attempt sequence changed: %d %d", one.AttemptNo, two.AttemptNo)
	}
	unknown := UsageEvidence{}
	if unknown.ReportedAmount != nil || unknown.EstimatedAmount != nil || unknown.RateSnapshot != nil {
		t.Fatal("unknown delivery/usage must not gain guessed money")
	}
}
