package dashboardapi

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

func TestEvaluateTurnsAlignmentHealthyWithinFloor(t *testing.T) {
	a := evaluateTurnsAlignment(1000, 1050)
	if a.Degraded {
		t.Fatalf("delta 50 under floor 100 must not degrade: %+v", a)
	}
	if a.AbsDelta != 50 {
		t.Fatalf("abs_delta=%d want 50", a.AbsDelta)
	}
}

func TestEvaluateTurnsAlignmentDegradedOverRelative(t *testing.T) {
	// 10% of 10000 = 1000; delta 1200 ⇒ degraded
	a := evaluateTurnsAlignment(10000, 8800)
	if !a.Degraded {
		t.Fatalf("expected degraded: %+v", a)
	}
	if a.Message == "" {
		t.Fatal("degraded must carry message")
	}
}

func TestBusinessTurnsFilterSQLUsesProbekindSSOT(t *testing.T) {
	got := businessTurnsFilterSQL("rl")
	if !strings.Contains(got, probekind.SQLStageList()) {
		t.Fatalf("filter must use probekind.SQLStageList SSOT, got %q", got)
	}
	if !strings.Contains(got, "probe_triggered") {
		t.Fatalf("filter must exclude probe_triggered, got %q", got)
	}
	// fmt.Sprintf turns SQL 'probe-%%' into the LIKE pattern probe-%
	if !strings.Contains(got, "NOT LIKE 'probe-%'") {
		t.Fatalf("filter must exclude probe-%% request_id, got %q", got)
	}
}
