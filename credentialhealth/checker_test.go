package credentialhealth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
)

func TestChecker_CheckAndUpdate_BelowThreshold(t *testing.T) {
	// Setup Redis + Recorder
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	//nolint:errcheck // best-effort close
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)

	// Setup mock DB
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	cfg := DefaultCheckerConfig()
	checker := NewChecker(recorder, mockDB, cfg)

	// Populate 10 calls: 7 success, 3 fail = 30% failure (below 80% threshold)
	ctx := context.Background()
	credID := 50
	model := "test-model"
	now := time.Now()

	for i := 0; i < 7; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_success",
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Success:   true,
			LatencyMs: 100,
		})
	}

	for i := 0; i < 3; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_fail",
			Timestamp: now.Add(time.Duration(7+i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: "quota",
		})
	}

	// No UPDATE expected (below threshold)
	err = checker.CheckAndUpdate(ctx, credID, model)
	if err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestChecker_CheckAndUpdate_AboveThreshold(t *testing.T) {
	// Setup Redis + Recorder
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	//nolint:errcheck // best-effort close
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)

	// Setup mock DB with a regex matcher so the test can match the
	// multi-line model_offers mirror UPDATE (which uses WHERE ... FROM ...
	// against provider_models and a SELECT subquery in the predicate).
	mockDB, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	cfg := DefaultCheckerConfig()
	checker := NewChecker(recorder, mockDB, cfg)

	// Populate 10 calls: 2 success, 8 fail = 80% failure (at threshold)
	ctx := context.Background()
	credID := 99
	model := "minimax-m3"
	now := time.Now()

	for i := 0; i < 2; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_success",
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Success:   true,
			LatencyMs: 100,
		})
	}

	for i := 0; i < 8; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_fail",
			Timestamp: now.Add(time.Duration(2+i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: "quota",
		})
	}

	// Expect UPDATE to credential_model_bindings (per-cred+model row),
	// NOT the credentials table. The cmb route is what v_routable_credential_models
	// reads; writing to credentials leaves the binding routable in production
	// even though the credential is "degraded" in the admin UI.
	mockDB.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// 2026-07-15 P1 fix: the model_offers mirror UPDATE no longer carries
	// the unavailable_recover_at placeholder; the remaining $1/$2 are
	// credential_id and unavailable_at for the cmb subquery join.
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*continuous_failure`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = checker.CheckAndUpdate(ctx, credID, model)
	if err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestChecker_CheckAndUpdate_ExcludeNetworkErrors(t *testing.T) {
	// Setup Redis + Recorder
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	//nolint:errcheck // best-effort close
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)

	// Setup mock DB
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	cfg := DefaultCheckerConfig()
	checker := NewChecker(recorder, mockDB, cfg)

	// Populate 10 calls: 5 network errors (excluded), 3 success, 2 quota fail
	// Non-network: 3 success + 2 fail = 40% (below 80%)
	ctx := context.Background()
	credID := 100
	model := "test"
	now := time.Now()

	for i := 0; i < 5; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_network",
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: "network", // excluded
		})
	}

	for i := 0; i < 3; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_success",
			Timestamp: now.Add(time.Duration(5+i) * time.Minute).UnixMilli(),
			Success:   true,
			LatencyMs: 100,
		})
	}

	for i := 0; i < 2; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_quota",
			Timestamp: now.Add(time.Duration(8+i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: "quota",
		})
	}

	// No UPDATE expected (40% failure after excluding network)
	err = checker.CheckAndUpdate(ctx, credID, model)
	if err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestChecker_CheckAndUpdate_ExcludeBenignEOF (2026-07-15 P0 regression test)
//
// Before the fix, the credentialhealth.Checker would treat every
// "eof_without_done" failure as evidence that the credential was unhealthy.
// MiniMax (and other SSE providers) routinely close streams without the
// [DONE] sentinel on otherwise-successful completions, so a healthy
// minimax-m3 binding on credential 21 was being pushed into a 15-minute
// cooldown whenever the upstream happened to skip [DONE] more than five
// times in an hour. This regression test pins the new behaviour: even
// when 100% of the recorded failures are eof_without_done, the checker
// must NOT write a degraded cooldown.
//
// The relay layer in domains/streaming/stream.go already marks
// eof_without_done as a successful completion from the caller's
// perspective, so success=true is the correct accounting — the exclude
// branch is what makes the failureRate 0% rather than 100%.
func TestChecker_CheckAndUpdate_ExcludeBenignEOF(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	//nolint:errcheck // best-effort close
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	checker := NewChecker(recorder, mockDB, DefaultCheckerConfig())

	// 10 failures, all eof_without_done. Before the fix this was 100%
	// failure rate → 15-minute cooldown. After the fix, all 10 are
	// excluded from the failureRate sample (treated like network/client
	// bugs) → no UPDATE expected.
	ctx := context.Background()
	credID := 121
	model := "MiniMax-M3"
	now := time.Now()

	for i := 0; i < 10; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_eof_" + now.Add(time.Duration(i)*time.Minute).Format(time.RFC3339),
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: "eof_without_done",
		})
	}

	if err := checker.CheckAndUpdate(ctx, credID, model); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("eof_without_done must be excluded from failure rate, but checker issued a DB write: %v", err)
	}
}

// TestChecker_CheckAndUpdate_MixedEOFStillFlagsTrueFailures ensures the
// exclude guard does not also swallow real credential failures mixed in
// with benign EOFs. 6 quota + 4 eof_without_done over 10 calls leaves
// 6 real failures out of 6 real samples = 100% > 80% threshold → one
// markDegraded UPDATE is expected.
func TestChecker_CheckAndUpdate_MixedEOFStillFlagsTrueFailures(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	//nolint:errcheck // best-effort close
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	// regex matcher for the same reason as AboveThreshold.
	mockDB, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	checker := NewChecker(recorder, mockDB, DefaultCheckerConfig())

	ctx := context.Background()
	credID := 122
	model := "MiniMax-M3"
	now := time.Now()

	for i := 0; i < 4; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_eof_" + now.Add(time.Duration(i)*time.Minute).Format(time.RFC3339),
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: "eof_without_done", // excluded
		})
	}

	for i := 0; i < 6; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_quota_" + now.Add(time.Duration(4+i)*time.Minute).Format(time.RFC3339),
			Timestamp: now.Add(time.Duration(4+i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: "quota", // real failure, must count
		})
	}

	// Expect: cmb UPDATE (the production source of truth).
	mockDB.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Expect: model_offers mirror UPDATE — but it must NOT carry an
	// unavailable_recover_at column (the column does not exist on the
	// view, see checker.go markDegraded comment). 2026-07-15 P1 fix
	// removed the third placeholder from this UPDATE entirely; the
	// remaining $1/$2 are the credential_id and unavailable_at timestamp
	// used to JOIN the cmb subquery.
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*continuous_failure`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := checker.CheckAndUpdate(ctx, credID, model); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("expected one cmb UPDATE + one model_offers mirror, got: %v", err)
	}
}

func TestRecoverExpired(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	// RecoverExpired must restore THREE state surfaces in a single tick:
	//  1. credential_model_bindings  (production router's source of truth)
	//  2. model_offers               (/api/routing/resolve "test route")
	//  3. credentials.availability_state  (candidate loader's v_routable filter)
	//
	// PR-3 T3 (2026-06-23): added the credentials.availability_state UPDATE
	// here to close the "cmb=TRUE but availability=cooling" false-negative
	// window. The third SQL mirrors the recovery in bg/credential_recovery.go
	// for defence-in-depth.
	mockDB.ExpectExec("UPDATE credential_model_bindings").
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mockDB.ExpectExec("UPDATE model_offers").
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mockDB.ExpectExec("UPDATE credentials").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	ctx := context.Background()
	count, err := RecoverExpired(ctx, mockDB)
	if err != nil {
		t.Fatalf("RecoverExpired failed: %v", err)
	}

	if count != 3 {
		t.Errorf("expected 3 recovered, got %d", count)
	}

	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRecoverExpired_HonoursRecoverAt(t *testing.T) {
	mockDB, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	defer mockDB.Close()
	mockDB.ExpectExec(`UPDATE credential_model_bindings[\s\S]*unavailable_recover_at`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*unavailable_at`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectExec(`UPDATE credentials`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if _, err := RecoverExpired(context.Background(), mockDB); err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("P0-A regression: %v", err)
	}
}

func TestRecoverExpired_SkipsModelProbeBroken(t *testing.T) {
	mockDB, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	defer mockDB.Close()
	mockDB.ExpectExec(`UPDATE credential_model_bindings[\s\S]*model_probe_broken`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*model_probe_broken`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectExec(`UPDATE credentials`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if _, err := RecoverExpired(context.Background(), mockDB); err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("model_probe_broken not excluded: %v", err)
	}
}
