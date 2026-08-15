package streaming

import (
	"net/http/httptest"
	"testing"
)

func validSnapshot() DurableRequestSnapshotV1 {
	return DurableRequestSnapshotV1{
		Version: 1, Endpoint: "/v1/chat/completions", ClientProtocol: "openai-chat",
		ClientModel: "gpt-x", NormalizedBody: []byte(`{"model":"gpt-x"}`), RequestHash: "hash",
		APIKeyID: 7, TenantID: "tenant-a", ApplicationID: 3, SessionID: "sess-a", SessionSource: "real",
		ClientIdentityHash: "client-hash", PolicyVersion: "p1", RequestID: "req-a", TaskCorrelationID: "task-a",
	}
}

func TestDurableSnapshotV1RoundTripAndSensitiveFieldsExcluded(t *testing.T) {
	body, err := MarshalDurableSnapshotV1(validSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || string(body) == "{}" {
		t.Fatal("snapshot must serialize")
	}
	for _, forbidden := range []string{"Authorization", "Cookie", "credential", "candidates"} {
		if string(body) == forbidden {
			t.Fatalf("forbidden field serialized: %s", forbidden)
		}
	}
	got, err := UnmarshalDurableSnapshotV1(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskCorrelationID != "task-a" || string(got.NormalizedBody) != `{"model":"gpt-x"}` {
		t.Fatalf("got=%+v", got)
	}
}

func TestDurableSnapshotV1RejectsUnknownVersionAndMissingRequired(t *testing.T) {
	cases := []DurableRequestSnapshotV1{
		func() DurableRequestSnapshotV1 { s := validSnapshot(); s.Version = 2; return s }(),
		func() DurableRequestSnapshotV1 { s := validSnapshot(); s.SessionID = ""; return s }(),
		func() DurableRequestSnapshotV1 { s := validSnapshot(); s.NormalizedBody = []byte("not-json"); return s }(),
	}
	for _, snapshot := range cases {
		if _, err := MarshalDurableSnapshotV1(snapshot); err == nil {
			t.Fatalf("invalid snapshot accepted: %+v", snapshot)
		}
	}
}

func TestClientCapabilitiesAndPreferParsing(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(GatewayCapabilitiesHeader, " DURABLE-RECOVERY, unknown, status-events, durable-recovery ")
	r.Header.Set("Prefer", "wait=10, respond-async; foo=bar")
	caps := ParseClientCapabilities(r.Header.Get(GatewayCapabilitiesHeader))
	if !caps.Has(CapabilityDurableRecovery) || !caps.Has(CapabilityStatusEvents) || len(caps.Tokens()) != 2 {
		t.Fatalf("caps=%v", caps.Tokens())
	}
	if !PreferRespondAsync(r) || !DurableRequested(r, false) {
		t.Fatal("explicit non-stream durable handshake not detected")
	}
	if DurableRequested(r, true) != true {
		t.Fatal("stream durable capability not detected")
	}
}

func TestDurableRequestedRequiresCapabilityAndPreferForNonStream(t *testing.T) {
	cases := []struct {
		caps, prefer string
		stream, want bool
	}{
		{"", "respond-async", false, false},
		{"durable-recovery", "", false, false},
		{"durable-recovery", "respond-async", false, true},
		{"durable-recovery", "", true, true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set(GatewayCapabilitiesHeader, tc.caps)
		r.Header.Set("Prefer", tc.prefer)
		if got := DurableRequested(r, tc.stream); got != tc.want {
			t.Errorf("caps=%q prefer=%q stream=%v got=%v want=%v", tc.caps, tc.prefer, tc.stream, got, tc.want)
		}
	}
}
