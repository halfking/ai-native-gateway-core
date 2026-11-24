package reconciliation

import (
	"encoding/json"
	"testing"
)

func TestDailyShadowCountChecks(t *testing.T) {
	summaries := map[string]json.RawMessage{
		"requests": json.RawMessage(`{"requests":2}`),
		"attempts": json.RawMessage(`{"sent":1}`),
		"postings": json.RawMessage(`{"postings":0}`),
		"coverage": json.RawMessage(`{"received_requests":2,"recorded_sends":2,"posted_entries":0}`),
	}
	checks, err := dailyShadowCountChecks(summaries)
	if err != nil {
		t.Fatal(err)
	}
	want := []DailyShadowCountCheck{
		{Name: "client_receipts", Report: 2, Source: 2, State: "equal"},
		{Name: "confirmed_supplier_sends", Report: 1, Source: 2, State: "different"},
		{Name: "request_posting_entries", State: "no_observations"},
	}
	if len(checks) != len(want) {
		t.Fatalf("checks: %+v", checks)
	}
	for i := range want {
		if checks[i] != want[i] {
			t.Errorf("check %d = %+v, want %+v", i, checks[i], want[i])
		}
	}
	summaries["attempts"] = json.RawMessage(`{}`)
	if _, err := dailyShadowCountChecks(summaries); err == nil {
		t.Fatal("missing selected count was treated as zero")
	}
}
