package reconciliation

import (
	"net/http"
	"testing"
)

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
