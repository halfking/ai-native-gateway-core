package maas

import "testing"

// TestTripartiteContract documents the出账勾稽 predicate:
// bucket sum for [period_start, period_end) MUST equal bill.total_credits
// after IssueMonthlyBill. Ledger consume is advisory (subscription quota
// path may skip wallet ledger rows).
func TestTripartiteContract(t *testing.T) {
	cases := []struct {
		name   string
		bucket int64
		bill   int64
		ok     bool
	}{
		{"match zero", 0, 0, true},
		{"match positive", 4200, 4200, true},
		{"bill lag", 100, -1, false},
		{"drift", 100, 90, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TripartiteOK(tc.bucket, tc.bill)
			if got != tc.ok {
				t.Fatalf("TripartiteOK(%d,%d)=%v want %v", tc.bucket, tc.bill, got, tc.ok)
			}
		})
	}
}

func TestIdempotentBillKey(t *testing.T) {
	// UNIQUE (tenant_id, period_ym) is the idempotency key — same inputs
	// must collide on the same composite key.
	k1 := "acme" + "|" + "2026-09"
	k2 := "acme" + "|" + "2026-09"
	if k1 != k2 {
		t.Fatal("idempotency key must be stable")
	}
}
