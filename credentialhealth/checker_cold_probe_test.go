package credentialhealth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
)

// fakeColdProber records the (credentialID, model) tuples it was asked
// to probe and returns a configurable ProbeResult. Used to verify the
// 2026-08-23 cold-node branch routes the right (cred, model) into the
// cold prober and short-circuits degradation on success.
type fakeColdProber struct {
	results map[string]ProbeResult
	seen    []struct{ CredID int; Model string }
}

func (f *fakeColdProber) ProbeCredential(_ context.Context, credID int, model string) ProbeResult {
	f.seen = append(f.seen, struct{ CredID int; Model string }{credID, model})
	if r, ok := f.results[model]; ok {
		return r
	}
	return ProbeResult{Success: false, Detail: "fake cold probe fail"}
}

func TestChecker_ColdProbeSuccessSkipsDegradation(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	coldProber := &fakeColdProber{
		results: map[string]ProbeResult{
			"test-model": {Success: true, Latency: 50 * time.Millisecond},
		},
	}
	cfg := DefaultCheckerConfig()
	cfg.ColdProber = coldProber
	checker := NewChecker(recorder, mockDB, cfg)

	// Empty recorder → no historical samples → enters the cold-probe branch.
	if err := checker.CheckAndUpdate(context.Background(), 50, "test-model"); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	if len(coldProber.seen) != 1 {
		t.Fatalf("cold prober should have been invoked exactly once, got %d", len(coldProber.seen))
	}
	if coldProber.seen[0].CredID != 50 || coldProber.seen[0].Model != "test-model" {
		t.Fatalf("cold prober saw unexpected pair: %+v", coldProber.seen[0])
	}
	// Success path must NOT mark degraded → no cmb UPDATE expected.
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("cold probe success path should not have hit the DB: %v", err)
	}
}

func TestChecker_ColdProbeFailureTriggersDegradation(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	coldProber := &fakeColdProber{
		results: map[string]ProbeResult{
			"test-model": {Success: false, ErrorKind: errorsx.KindUpstreamDown, Detail: "fake fail"},
		},
	}
	cfg := DefaultCheckerConfig()
	cfg.ColdProber = coldProber
	checker := NewChecker(recorder, mockDB, cfg)

	expectRawBindingResolution(mockDB, 50, "test-model", "test-model")
	mockDB.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*continuous_failure`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := checker.CheckAndUpdate(context.Background(), 50, "test-model"); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestChecker_NoColdProbeWiredReturnsEarlyOnEmptySamples(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	cfg := DefaultCheckerConfig()
	cfg.ColdProber = nil // explicitly nil: legacy behaviour
	checker := NewChecker(recorder, mockDB, cfg)

	if err := checker.CheckAndUpdate(context.Background(), 50, "test-model"); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	// No samples, no cold prober → no DB writes.
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("legacy empty-samples path should not hit DB: %v", err)
	}
}

// TestChecker_DominantKindGradient exercises the 2026-08-23 gradient
// path: a credential whose recent failures are dominated by
// KindRateLimit must use the rate_limit threshold (95%), not the global
// 80%. The test seeds 10 samples (1 success, 9 rate_limit failures = 90%
// failure rate) — below the rate_limit 95% threshold, so the legacy
// behaviour would NOT degrade; the gradient preserves this by raising
// the threshold for rate_limit-dominated credentials.
func TestChecker_DominantKindGradient(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	cfg := DefaultCheckerConfig()
	checker := NewChecker(recorder, mockDB, cfg)

	ctx := context.Background()
	credID := 50
	model := "test-model"
	now := time.Now()

	// 9 rate_limit failures + 1 success = 90% failure. The global 80%
	// threshold would trip; the rate_limit gradient (95%) must NOT trip.
	if err := recorder.Append(ctx, credID, model, CallEntry{
		RequestID: "req_ok", Timestamp: now.Add(0 * time.Minute).UnixMilli(),
		Success: true, LatencyMs: 100,
	}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("recorder.Append ok failed: %v", err)
	}
	for i := 0; i < 9; i++ {
		if err := recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_rl", Timestamp: now.Add(time.Duration(1+i) * time.Minute).UnixMilli(),
			Success: false, ErrorKind: "rate_limit",
		}); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("recorder.Append rl failed: %v", err)
		}
	}

	if err := checker.CheckAndUpdate(ctx, credID, model); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	// Gradient preserved the credential — no DB writes expected.
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("rate_limit gradient should not have degraded at 90%%: %v", err)
	}
}
