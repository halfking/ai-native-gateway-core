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
	//
	// 2026-10-01: this used to assert the literal call
	// `ensureCandidateFailureLogsHeapPartitions(migCtx)`, which pinned the
	// *variable* rather than the intent. Detaching this one step from the
	// 3-minute chain-wide migCtx (production evidence: the shared budget was
	// exhausted upstream and the conversion was killed with `context deadline
	// exceeded`, aborting the whole ensure chain → postgres disabled →
	// live-stream hub without DB → dashboard model groups permanently
	// hidden) necessarily changed the argument to a dedicated budget context.
	// The wiring is still there — that is what must not drift.
	//
	// Assert the intent: the call exists, it is inside the ensure chain, and
	// it is still error-checked (aborting boot on real failure) rather than
	// silently swallowed.
	// Take the whole line (not just from the match point), so the `if err :=`
	// guard that precedes the call is included in the assertion.
	callIdx := strings.Index(text, "db.ensureCandidateFailureLogsHeapPartitions(")
	if callIdx < 0 {
		t.Fatal("db.go must call ensureCandidateFailureLogsHeapPartitions in applyMigrationsOnce")
	}
	lineStart := strings.LastIndexByte(text[:callIdx], '\n') + 1
	callLine := text[lineStart:]
	if end := strings.IndexByte(callLine, '\n'); end >= 0 {
		callLine = callLine[:end]
	}
	// Still inside an `if err := ...; err != nil { return err }` guard.
	if !strings.HasPrefix(strings.TrimSpace(callLine), "if err := db.ensureCandidateFailureLogsHeapPartitions(") {
		t.Fatalf("ensureCandidateFailureLogsHeapPartitions must stay error-checked and aborting boot; got %q", callLine)
	}
	// It must receive a context — but not necessarily the chain-wide migCtx.
	// Reject an un-timed context, which would let the step hang forever.
	const callPrefix = "if err := db.ensureCandidateFailureLogsHeapPartitions("
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(callLine), callPrefix))
	if end := strings.Index(arg, ");"); end >= 0 {
		arg = strings.TrimSpace(arg[:end])
	}
	if arg == "" || !strings.HasSuffix(arg, "Ctx") {
		t.Fatalf("ensureCandidateFailureLogsHeapPartitions must be called with a timeout context variable (…Ctx), got %q", arg)
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
