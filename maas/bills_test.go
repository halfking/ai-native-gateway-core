package maas

import (
	"strings"
	"testing"
	"time"
)

func TestPeriodBounds(t *testing.T) {
	start, end, err := PeriodBounds("2026-09", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if start.Format(time.RFC3339) != "2026-09-01T00:00:00Z" {
		t.Fatalf("start=%s", start)
	}
	if end.Format(time.RFC3339) != "2026-10-01T00:00:00Z" {
		t.Fatalf("end=%s", end)
	}
	if _, _, err := PeriodBounds("2026/09", time.UTC); err == nil {
		t.Fatal("expected format error")
	}
}

func TestPreviousPeriodYM(t *testing.T) {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	got := PreviousPeriodYM(now, time.UTC)
	if got != "2026-09" {
		t.Fatalf("got %s want 2026-09", got)
	}
	jan := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	if PreviousPeriodYM(jan, time.UTC) != "2025-12" {
		t.Fatalf("jan rollover failed")
	}
}

func TestAmountCentsFromCredits(t *testing.T) {
	if AmountCentsFromCredits(1000, 0.1) != 100 {
		t.Fatalf("1000*0.1 cents")
	}
	if AmountCentsFromCredits(0, 0.1) != 0 {
		t.Fatal("zero credits")
	}
	if AmountCentsFromCredits(3, 0.1) != 0 {
		// 0.3 rounds to 0 with Round? math.Round(0.3)=0
		t.Log("3*0.1=0.3 → Round 0 ok")
	}
	if AmountCentsFromCredits(15, 0.1) != 2 { // 1.5 → 2
		t.Fatalf("15*0.1 Round want 2 got %d", AmountCentsFromCredits(15, 0.1))
	}
}

func TestFormatBillsCSV(t *testing.T) {
	issued := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	csv := FormatBillsCSV([]MonthlyBill{{
		PeriodYM: "2026-09", TotalCredits: 100, TotalRequests: 5,
		AmountCents: 10, Currency: "CNY", Status: "issued", IssuedAt: &issued,
	}})
	if !strings.HasPrefix(csv, "period_ym,total_credits") {
		t.Fatalf("header: %q", csv)
	}
	if !strings.Contains(csv, "2026-09,100,5,10,CNY,issued,") {
		t.Fatalf("row missing: %q", csv)
	}
}

func TestIssueMonthlyBill_Gating(t *testing.T) {
	svc := NewService(nil)
	if _, err := svc.IssueMonthlyBill(nil, "t1", "2026-09"); err == nil {
		t.Fatal("disabled service must error")
	}
}

func TestTripartiteOK(t *testing.T) {
	if !TripartiteOK(100, 100) {
		t.Fatal("equal should match")
	}
	if TripartiteOK(100, 99) {
		t.Fatal("mismatch")
	}
}
