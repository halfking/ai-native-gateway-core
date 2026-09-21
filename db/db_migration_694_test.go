package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigration694SelfHealPinsShanghaiTimezone guards the convergence of the
// 689 startup self-heal with migration 694's final candidate-failure function
// body. ensureCandidateFailureLogsHeapPartitions reruns on every binary
// startup; if its mirrored body drifts back to DECLARE-time date_trunc
// initialization, it overwrites the Asia/Shanghai-pinned function that
// migration 694 installed and resurrects the 473-class 8h partition gap under
// UTC sessions.
func TestMigration694SelfHealPinsShanghaiTimezone(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	// The self-heal must stay wired in applyMigrationsOnce.
	if !strings.Contains(text, "ensureCandidateFailureLogsHeapPartitions(migCtx)") {
		t.Fatal("db.go must call ensureCandidateFailureLogsHeapPartitions in applyMigrationsOnce")
	}

	// The mirrored candidate function must converge to 694's final body:
	// pin the timezone as the first statement, then derive the month.
	const sqlConst = "const ensureCandidateFailureLogsHeapPartitionsSQL = "
	constStart := strings.Index(text, sqlConst)
	if constStart < 0 {
		t.Fatal("db.go missing ensureCandidateFailureLogsHeapPartitionsSQL constant")
	}
	// The constant runs to the closing backtick before the wrapper method.
	constEnd := strings.Index(text[constStart:], "func (d *DB) ensureCandidateFailureLogsHeapPartitions(")
	if constEnd < 0 {
		t.Fatal("db.go missing ensureCandidateFailureLogsHeapPartitions wrapper")
	}
	body := text[constStart : constStart+constEnd]

	setIdx := strings.Index(body, "SET LOCAL TIME ZONE 'Asia/Shanghai';")
	if setIdx < 0 {
		t.Fatal("689 self-heal SQL must pin SET LOCAL TIME ZONE 'Asia/Shanghai' (migration 694 convergence)")
	}
	if truncIdx := strings.Index(body, "date_trunc('month', target_ts)"); truncIdx >= 0 && truncIdx < setIdx {
		t.Fatal("689 self-heal SQL derives the month before the timezone pin — it would overwrite migration 694 on every startup")
	}

	// 694 moved all month derivation out of DECLARE initializers: those
	// evaluate before the function body, so no in-body SET LOCAL can save
	// them. Forbid the pre-694 shape outright.
	for _, stale := range []string{
		"month_start    date := date_trunc(",
		"month_start date := date_trunc(",
	} {
		if strings.Contains(body, stale) {
			t.Fatalf("689 self-heal SQL still uses a DECLARE-time initializer %q; month derivation must move behind the timezone pin", stale)
		}
	}

	// Storage contract from 689 is unchanged: heap only, no columnar revival.
	fnBody := body[strings.Index(body, "CREATE OR REPLACE FUNCTION public.ensure_candidate_failure_logs_partition"):]
	if strings.Contains(fnBody, "USING columnar") || strings.Contains(fnBody, "enforce_columnar_partition") {
		t.Error("689 self-heal candidate function must keep heap semantics")
	}
	if !strings.Contains(fnBody, "RETURNS text") {
		t.Error("689 self-heal candidate function must keep RETURNS text (689 signature)")
	}
}
