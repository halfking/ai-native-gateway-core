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

	// Setup mock DB
	mockDB, err := pgxmock.NewPool()
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
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*unavailable_recover_at`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectExec(`UPDATE credentials`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if _, err := RecoverExpired(context.Background(), mockDB); err != nil { t.Fatalf("RecoverExpired: %v", err) }
	if err := mockDB.ExpectationsWereMet(); err != nil { t.Errorf("P0-A regression: %v", err) }
}

func TestRecoverExpired_SkipsModelProbeBroken(t *testing.T) {
	mockDB, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	defer mockDB.Close()
	mockDB.ExpectExec(`UPDATE credential_model_bindings[\s\S]*model_probe_broken`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*model_probe_broken`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectExec(`UPDATE credentials`).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if _, err := RecoverExpired(context.Background(), mockDB); err != nil { t.Fatalf("RecoverExpired: %v", err) }
	if err := mockDB.ExpectationsWereMet(); err != nil { t.Errorf("model_probe_broken not excluded: %v", err) }
}
