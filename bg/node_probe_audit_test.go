package bg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestEmitSyncAuditReturnsInsertFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec("INSERT INTO node_probe_runs").
		WithArgs(
			7, "raw-model",
			true, 200, "", 15, "",
			true, 200, "", 16, "",
			true, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			"raw-model", "outbound-model", 7,
			"https://provider.example/v1/chat/completions", pgxmock.AnyArg(), "request", "response",
			15, false,
			"parent-request",
		).
		WillReturnError(errors.New("audit schema unavailable"))

	worker := &NodeProbeWorker{auditDB: mock}
	err = worker.emitSyncAudit(context.Background(), 7, "raw-model",
		nodeProbeRoundResult{
			ok: true, providerID: 7, outboundModel: "outbound-model", httpStatus: 200,
			latencyMs: 15, requestURL: "https://provider.example/v1/chat/completions",
			requestHeaders: map[string]string{"Authorization": "secret", "X-Test": "allowed"},
			requestBody:    "request", responseBody: "response",
		},
		nodeProbeRoundResult{ok: true, httpStatus: 200, latencyMs: 16},
		time.Now().Add(-time.Second), "parent-request")
	if err == nil || !strings.Contains(err.Error(), "audit schema unavailable") {
		t.Fatalf("emitSyncAudit error = %v, want wrapped insert failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestProbeHeadersJSONReturnsJSONText(t *testing.T) {
	if got := probeHeadersJSON(nil); got != "{}" {
		t.Fatalf("probeHeadersJSON(nil) = %q, want {}", got)
	}
	got := probeHeadersJSON(map[string]string{"X-Test": "allowed"})
	if got != `{"X-Test":"allowed"}` {
		t.Fatalf("probeHeadersJSON() = %q", got)
	}
}

// R12 P2: node_probe_runs_attempt_check rejects attempt outside 1..7, and the
// legacy runOne attempt (consecutive_failures+1) grows past 7 once the 7-attempt
// pause was removed. The audit write must clamp instead of failing (23514).
func TestClampAuditAttempt(t *testing.T) {
	cases := []struct{ in, want int }{
		{-3, 1}, {0, 1}, {1, 1}, {4, 4}, {7, 7}, {8, 7}, {12, 7},
	}
	for _, c := range cases {
		if got := clampAuditAttempt(c.in); got != c.want {
			t.Fatalf("clampAuditAttempt(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestInsertNodeProbeRunClampsAttempt(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec("INSERT INTO node_probe_runs").
		WithArgs(
			7, "raw-model", "request_failure", 7, 300,
			false, 0, "", 0, "",
			false, 0, "", 0, "",
			false, pgxmock.AnyArg(), pgxmock.AnyArg(), 5,
			"raw-model", "", 0,
			"", "{}", "", "",
			0, false,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	worker := &NodeProbeWorker{auditDB: mock}
	// attempt=11 would violate the CHECK (BETWEEN 1 AND 7); the insert must
	// receive the clamped value 7.
	if err := worker.insertNodeProbeRun(context.Background(), 7, "raw-model", "request_failure",
		11, 300, nodeProbeRoundResult{}, nodeProbeRoundResult{}, false,
		time.Now().Add(-5*time.Millisecond), time.Now(), 5); err != nil {
		t.Fatalf("insertNodeProbeRun(attempt=11) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// R51 审计 P3：panic 重启退避策略。原实现间隔只增不减（几次 panic 后永久
// 钉在"封顶"值——且封顶判断有误，240s×2=8min 超过宣称的 5min）；现健康
// 运行（≥3 tick）后的 panic 重置到起步值，短命 panic 指数翻倍且真正封顶。
func TestNextProbeLoopBackoff(t *testing.T) {
	cases := []struct {
		name        string
		prevBackoff time.Duration
		previousRun time.Duration
		want        time.Duration
	}{
		{"first short panic doubles", nodeProbeLoopInitialBackoff, 0, 60 * time.Second},
		{"short run keeps doubling", 4 * time.Minute, time.Second, nodeProbeLoopMaxBackoff},
		{"cap is a true 5min", nodeProbeLoopMaxBackoff, time.Second, nodeProbeLoopMaxBackoff},
		{"never exceeds 5min (240s case)", 4 * time.Minute, 0, nodeProbeLoopMaxBackoff},
		{"healthy run resets to initial", nodeProbeLoopMaxBackoff, 3 * nodeProbeTickInterval, nodeProbeLoopInitialBackoff},
		{"healthy run after one panic resets", 60 * time.Second, 91 * time.Second, nodeProbeLoopInitialBackoff},
		{"just under healthy age still doubles", nodeProbeLoopInitialBackoff, 89 * time.Second, 60 * time.Second},
	}
	for _, tc := range cases {
		if got := nextProbeLoopBackoff(tc.prevBackoff, tc.previousRun); got != tc.want {
			t.Errorf("%s: nextProbeLoopBackoff(%v, %v) = %v, want %v",
				tc.name, tc.prevBackoff, tc.previousRun, got, tc.want)
		}
	}
}
