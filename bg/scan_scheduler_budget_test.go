package bg

// R36 (2026-09-17 audit) — pins the environment contract behind
// scanCutBySweepBudget: the R34 exemption branch additionally tested
// errors.Is(err, context.DeadlineExceeded), which since Go ≥1.23 also
// matches http.Client.Timeout errors. The FreeDiscovery scanner runs a
// 30s per-template client timeout (safehttpclient.New), so a hung upstream
// satisfied the "budget cut" branch, recordScanFailure never fired, and the
// 3-strike template auto-disable was unreachable for slow providers.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestScanBudgetExempt_ClientTimeoutCountsAsFailure proves that a client
// timeout error (a) satisfies errors.Is(err, context.DeadlineExceeded) — the
// trap the R34 condition fell into — and (b) is NOT classified as a sweep
// budget cut while the parent context is alive, so recordScanFailure runs.
func TestScanBudgetExempt_ClientTimeoutCountsAsFailure(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := &http.Client{Timeout: 150 * time.Millisecond}
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("precondition changed: Client.Timeout no longer wraps DeadlineExceeded (err=%v); re-evaluate scanCutBySweepBudget", err)
	}
	ctx := context.Background() // parent sweep context still alive
	if scanCutBySweepBudget(ctx) {
		t.Fatal("client timeout with live parent context must count as template failure, not budget cut")
	}
}

// TestScanBudgetExempt_SweepCtxCanceled proves the legitimate budget-cut
// path: an expired/canceled parent context is exempt regardless of error.
func TestScanBudgetExempt_SweepCtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !scanCutBySweepBudget(ctx) {
		t.Fatal("canceled parent context must be exempt as sweep budget cut")
	}
}
