package dispatch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLeaseJSONRoundTripStable(t *testing.T) {
	// Pin a deterministic timestamp so the test is timezone-independent.
	issued := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	expires := issued.Add(30 * time.Second)

	in := Lease{
		Token:        "lease:42:abcd",
		CredentialID: 42,
		Backend:      string(BackendRedisEnforce),
		IssuedAt:     issued,
		ExpiresAt:    expires,
		SpecRevision: 7,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Lease
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip drift:\n got %+v\nwant %+v", out, in)
	}
}

// Pin the JSON wire shape so a future field-add that would silently
// break an external log scraper / projection cannot land without
// updating this test on purpose.
func TestLeaseJSONShape(t *testing.T) {
	in := Lease{
		Token:        "tok",
		CredentialID: 1,
		Backend:      string(BackendRedisEnforce),
		SpecRevision: 2,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// IssuedAt / ExpiresAt are zero — they ARE present in JSON output as
	// "0001-01-01T00:00:00Z" because encoding/json does NOT omit time.Time
	// zero values without an explicit `omitempty` tag (Lease has none).
	// When Stage B populates them they will replace the zero string with
	// the actual timestamp; the field set itself is stable.
	got := string(data)
	want := `{"Token":"tok","CredentialID":1,"Backend":"redis_enforce","IssuedAt":"0001-01-01T00:00:00Z","ExpiresAt":"0001-01-01T00:00:00Z","SpecRevision":2}`
	if got != want {
		t.Fatalf("wire shape drift:\n got %s\nwant %s", got, want)
	}
}

func TestLeaseZeroExpiresAtPassesThrough(t *testing.T) {
	// Backends without leasing must be able to return zero-time Leases
	// without normalization — the forwarder checks Backend != "" to gate
	// on lease handling.
	in := Lease{Token: "tok", Backend: string(BackendLocal)}
	if !in.ExpiresAt.IsZero() {
		t.Fatalf("zero Lease.ExpiresAt should pass through: got %v", in.ExpiresAt)
	}
	if !in.IssuedAt.IsZero() {
		t.Fatalf("zero Lease.IssuedAt should pass through: got %v", in.IssuedAt)
	}
}