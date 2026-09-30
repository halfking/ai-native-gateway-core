//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// classCase is one request_logs row shape fed to BOTH classifiers: the SQL
// expression in mirrorDriftClassSQL and the Go predicate in
// telemetry.IsInternalAutoEntry. The two must agree on the loopback arm —
// that agreement is the whole point of the test.
type classCase struct {
	name         string
	isAuto       *bool
	originActor  *string
	requestType  *string
	taskType     *string
	workType     *string
	success      bool
	requestStat  *string
	errorKind    *string
	wantLoopback bool // Go's IsInternalAutoEntry
}

func sptr(s string) *string { return &s }

// TestMirrorDriftClassSQL_MatchesIsInternalAutoEntry is the behavioural
// guard behind the S4 gate.
//
// The string-presence test next door can only prove the SQL *mentions*
// origin_actor; it cannot prove the SQL *agrees with Go* about which rows the
// mirror hook actually skips. That divergence is not hypothetical — the
// first cut of this expression keyed internal_loopback on work_type while
// IsInternalAutoEntry (telemetry/internal_loopback.go:23-39) keys on
// is_auto_request + request_type / origin_actor / task_type. Rows with an
// unstamped work_type were then reported as genuine_loss forever, pinning
// s4_ready=false and blocking a cutover that was in fact clean.
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./cmd/gateway/ -run TestMirrorDriftClassSQL_MatchesIsInternalAutoEntry
func TestMirrorDriftClassSQL_MatchesIsInternalAutoEntry(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the SQL and the Go gate agree")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	autoTrue, autoFalse := true, false
	cases := []classCase{
		{
			name: "generator actor + stamped work_type", isAuto: &autoTrue,
			originActor: sptr("auto-title-generator"), requestType: sptr("main"),
			workType: sptr("session_title"), success: true, requestStat: sptr("success"),
			wantLoopback: true,
		},
		{
			// The 2026-09-30 measured shape: generator actor but is_auto_request
			// NULL. Go returns false on the very first line, so the hook MIRRORS
			// it — an unmirrored row here is real loss, not a design exclusion.
			name: "generator actor with NULL is_auto_request", isAuto: nil,
			originActor: sptr("auto-summary-generator"), requestType: sptr("main"),
			success: true, requestStat: sptr("success"),
			wantLoopback: false,
		},
		{
			// Business auto-route: is_auto_request TRUE + task_type set. The Go
			// comment is explicit that these MUST be mirrored so the task
			// dimension stays queryable.
			name: "business auto-route with task_type", isAuto: &autoTrue,
			originActor: nil, requestType: sptr("main"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"),
			wantLoopback: false,
		},
		{
			name: "taskless auto entry", isAuto: &autoTrue,
			originActor: nil, requestType: sptr("main"), taskType: nil,
			success: true, requestStat: sptr("success"),
			wantLoopback: true,
		},
		{
			name: "request_type title_gen", isAuto: &autoTrue,
			originActor: nil, requestType: sptr("title_gen"), taskType: sptr("code"),
			success: true, requestStat: sptr("success"),
			wantLoopback: true,
		},
		{
			name: "is_auto_request false", isAuto: &autoFalse,
			originActor: sptr("auto-title-generator"), requestType: sptr("main"),
			success: true, requestStat: sptr("success"),
			wantLoopback: false,
		},
		{
			// work_type stamped on a business turn: the Go gate never reads it,
			// so the SQL must not treat it as an exclusion either.
			name: "stamped work_type on business turn", isAuto: &autoTrue,
			originActor: nil, requestType: sptr("main"), taskType: sptr("code"),
			workType: sptr("session_title"), success: true, requestStat: sptr("success"),
			wantLoopback: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &telemetry.RequestLogEntry{
				IsAutoRequest: tc.isAuto,
				OriginActor:   tc.originActor,
				RequestType:   tc.requestType,
				TaskType:      tc.taskType,
				Success:       tc.success,
				RequestStatus: tc.requestStat,
				ErrorKind:     tc.errorKind,
			}
			goInternal := telemetry.IsInternalAutoEntry(entry)

			var got string
			err := pool.QueryRow(ctx, `
SELECT `+mirrorDriftClassSQL+`
FROM (VALUES
    ($1::boolean, $2::text, $3::text, $4::text, $5::text, $6::boolean, $7::text, $8::text)
) AS rl(is_auto_request, origin_actor, request_type, task_type, work_type,
        success, request_status, error_kind)`,
				tc.isAuto, tc.originActor, tc.requestType, tc.taskType, tc.workType,
				tc.success, tc.requestStat, tc.errorKind,
			).Scan(&got)
			if err != nil {
				t.Fatalf("class SQL execution failed: %v", err)
			}

			sqlInternal := got == "internal_loopback"
			if goInternal != tc.wantLoopback {
				t.Fatalf("fixture drift: Go says IsInternalAutoEntry=%v, fixture declares %v — the Go gate changed, refresh the case",
					goInternal, tc.wantLoopback)
			}
			if sqlInternal != goInternal {
				t.Errorf("classifiers disagree: SQL=%q (internal=%v) vs Go IsInternalAutoEntry=%v\n"+
					"a disagreement here either hides real loss or blocks a safe cutover",
					got, sqlInternal, goInternal)
			}
		})
	}
}

// TestMirrorDriftClassSQL_NonTerminalArm pins the second exclusion arm against
// the hook's terminal gate (hook.go:71 + isTerminalFailure at hook.go:991).
// The first cut keyed this arm on request_status='in_progress' only, which
// misfiled every failure row carrying a NULL request_status.
func TestMirrorDriftClassSQL_NonTerminalArm(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence for the non_terminal arm")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	cases := []struct {
		name        string
		success     bool
		requestStat *string
		errorKind   *string
		want        string
	}{
		{"in_progress placeholder", false, sptr("in_progress"), nil, "non_terminal"},
		{"NULL status and no error kind", false, nil, nil, "non_terminal"},
		{"empty error kind", false, sptr("in_progress"), sptr(""), "non_terminal"},
		{"terminal failure", false, sptr("failure"), sptr("provider_error"), "genuine_loss"},
		{"rate limited", false, sptr("rate_limited"), sptr("rate_limit_exceeded"), "genuine_loss"},
		{"success", true, sptr("success"), nil, "genuine_loss"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			err := pool.QueryRow(ctx, `
SELECT `+mirrorDriftClassSQL+`
FROM (VALUES
    (NULL::boolean, NULL::text, 'main'::text, NULL::text, NULL::text, $1::boolean, $2::text, $3::text)
) AS rl(is_auto_request, origin_actor, request_type, task_type, work_type,
        success, request_status, error_kind)`,
				tc.success, tc.requestStat, tc.errorKind,
			).Scan(&got)
			if err != nil {
				t.Fatalf("class SQL execution failed: %v", err)
			}
			if got != tc.want {
				t.Errorf("class = %q, want %q", got, tc.want)
			}
		})
	}
}
