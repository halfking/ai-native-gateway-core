package credentialhealth

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/errorsx"
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// 2026-08-11 fix: the model_offers mirror UPDATE now carries THREE
	// placeholders ($1 credential_id, $2 canonical_raw_name, $3 now) so the
	// cmb subquery can join on the exact unavailable_at timestamp written to
	// cmb (previously it joined on recoverAt and never matched).
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*continuous_failure`).
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

// TestChecker_CheckAndUpdate_ExcludeBenignEOF — stream_timeout NOW counted.
//
// 2026-07-15: KindStreamTimeout was excluded from failureRate because
// "EOF without [DONE]" on SSE streams from MiniMax was benign. Genuinely
// benign EOFs (ChunkCount>0) are short-circuited as success in
// executor_chat.go:687 and never reach the recorder.
//
// 2026-07-16: The exclusion is REMOVED. Non-benign StreamTimeouts (no
// chunks, first-byte timeout) ARE a credential health signal; the
// exclusion was the root cause of "NIM times out but never fails over".
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

	// 10 failures, all stream_timeout (the classified kind for non-benign
	// EOF / first-byte timeout). After the 2026-07-16 P1 fix, stream_timeout
	// IS counted as credential failure — non-benign StreamTimeouts (no chunks)
	// are a legitimate health signal. Truly benign EOFs (ChunkCount>0) are
	// short-circuited as success in executor_chat.go and never reach the
	// recorder, so this test's data represents the non-benign tail.
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
			ErrorKind: string(errorsx.KindStreamTimeout),
		})
	}

	// 2026-07-16 P1: stream_timeout IS counted → 10/10 = 100% > 80% → markDegraded fires.
	// 2026-08-11 fix: cmb UPDATE now carries 4 placeholders
	// ($1 credential_id, $2 model, $3 recoverAt, $4 now) and the
	// model_offers mirror carries 3 ($1 credential_id, $2 model, $3 now).
	mockDB.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*continuous_failure`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := checker.CheckAndUpdate(ctx, credID, model); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("stream_timeout triggered unexpected DB state: %v", err)
	}
}

// TestChecker_CheckAndUpdate_MixedEOFStillFlagsTrueFailures ensures the
// exclude guard does not also swallow real credential failures mixed in
// with benign EOFs. 6 quota + 4 stream_timeout over 10 calls leaves
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
			ErrorKind: string(errorsx.KindStreamTimeout), // excluded
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
	// 2026-08-11 fix: cmb UPDATE now carries 4 placeholders.
	mockDB.ExpectExec("UPDATE credential_model_bindings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Expect: model_offers mirror UPDATE — 2026-08-11 fix: the mirror now
	// matches on (credential_id, canonical_raw_name) and passes the exact
	// now() timestamp written to cmb.unavailable_at, so it carries 3
	// placeholders ($1 credential_id, $2 model, $3 now).
	mockDB.ExpectExec(`UPDATE model_offers[\s\S]*continuous_failure`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := checker.CheckAndUpdate(ctx, credID, model); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("expected one cmb UPDATE + one model_offers mirror, got: %v", err)
	}
}

// TestChecker_CheckAndUpdate_ExcludeEmptyResponse — empty_response is
// excluded from the failureRate computation. This guards the 2026-07-18
// regression where credential 19 (NVIDIA NIM / endless) accumulated
// 21 empty_response samples out of 34 calls (62% absolute rate) and was
// marked degraded with unavailable_recover_at = now+1h, even though
// the upstream was healthy. errorsx.KindEmptyResponse's design intent
// (classify.go line 60-78) explicitly says "a transient empty burst
// must not hard-exclude the credential". Counting it toward the 80%
// degradation threshold defeats that intent.
//
// 10 empty_response + 10 success: failureRate after exclusion = 0/20 = 0%
// → no DB UPDATE expected (markDegraded must not fire).
func TestChecker_CheckAndUpdate_ExcludeEmptyResponse(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	//nolint:errcheck // best-effort close
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	// Plain QueryMatcher (not regex) so we can assert NO UPDATE is issued
	// — pgxmock fails ExpectationsWereMet when an expected Exec was never
	// called.
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	checker := NewChecker(recorder, mockDB, DefaultCheckerConfig())

	ctx := context.Background()
	credID := 123
	model := "minimaxai/minimax-m3"
	now := time.Now()

	for i := 0; i < 10; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_empty_" + now.Add(time.Duration(i)*time.Minute).Format(time.RFC3339),
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Success:   false,
			ErrorKind: string(errorsx.KindEmptyResponse),
		})
	}
	for i := 0; i < 10; i++ {
		//nolint:errcheck // test append, non-critical
		recorder.Append(ctx, credID, model, CallEntry{
			RequestID: "req_ok_" + now.Add(time.Duration(10+i)*time.Minute).Format(time.RFC3339),
			Timestamp: now.Add(time.Duration(10+i) * time.Minute).UnixMilli(),
			Success:   true,
			LatencyMs: 300,
		})
	}

	if err := checker.CheckAndUpdate(ctx, credID, model); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	// empty_response is excluded → 0/10 counted failures (the 10 successes
	// are not failures) → failureRate = 0% < 80% threshold → no UPDATE
	// should be issued. ExpectationsWereMet returns nil only when every
	// expected query was matched; with zero expectations on a plain
	// (non-regex) matcher, this proves no UPDATE was issued.
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("empty_response triggered unexpected DB state: %v", err)
	}
}

// Upstream overload is a provider capacity signal, not a credential defect.
// It must remain observable without cooling the binding out of routing.
func TestChecker_CheckAndUpdate_ExcludeUpstreamOverloaded(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	recorder := NewRecorder(redisClient, time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mockDB.Close()

	checker := NewChecker(recorder, mockDB, DefaultCheckerConfig())
	now := time.Now()
	for i := 0; i < 10; i++ {
		err := recorder.Append(context.Background(), 124, "gpt-5.6-luna", CallEntry{
			RequestID: "req_overload",
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			ErrorKind: string(errorsx.KindUpstreamOverloaded),
		})
		if err != nil {
			t.Fatalf("record overload: %v", err)
		}
	}

	if err := checker.CheckAndUpdate(context.Background(), 124, "gpt-5.6-luna"); err != nil {
		t.Fatalf("CheckAndUpdate failed: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("upstream overload must not trigger binding degradation: %v", err)
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

// TestRecoverExpired_SuspendedSQLGuard pins the 2026-08-07 P0 fix
// to RecoverExpired(): the third UPDATE (credentials.availability_state)
// must whitelist 'suspended' AND refuse to flip it while quota_state
// is still hard-failed.
//
// Production deadlock (cred 22 zhipu-roocode-v2, cred 34 zhima-1):
//
//	availability_state='suspended' with availability_recover_at
//	elapsed — but the original RecoverExpired() did not include
//	'suspended' in its IN list. Same one-way lock as the 60s ticker.
//	Defence-in-depth: RecoverExpired must be a true backup of the
//	60s recover() path.
//
// We assert by reading the source: the credentials UPDATE must
// (a) include 'suspended' in availability_state IN(...) and
// (b) gate it on quota_state NOT IN (permanently_exhausted,
//
//	balance_exhausted) so true hard-quota creds cannot be flipped
//	by a single RecoverExpired tick.
func TestRecoverExpired_SuspendedSQLGuard(t *testing.T) {
	src, err := os.ReadFile("checker.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	// (a) 'suspended' must be in the availability_state whitelist.
	pattern := regexp.MustCompile(`availability_state\s+IN\s*\([^)]*'suspended'[^)]*\)`)
	if !pattern.MatchString(body) {
		t.Fatalf("RecoverExpired availability_state IN(...) does NOT include 'suspended' — 2026-08-07 P0 regression")
	}
	// (b) Hard-quota guard present in the suspended branch.
	guardPattern := regexp.MustCompile(
		`availability_state\s*<>\s*'suspended'\s*` +
			`OR\s+COALESCE\(quota_state,\s*'ok'\)\s*NOT\s+IN\s*\(\s*'permanently_exhausted',\s*'balance_exhausted'\s*\)`,
	)
	if !guardPattern.MatchString(body) {
		t.Fatalf("RecoverExpired suspended hard-quota guard missing — balance/permanent creds would auto-recover:\n%s",
			extractSnippet(body, "suspended"))
	}
	// (c) Original whitelist states still present.
	for _, s := range []string{"'cooling'", "'rate_limited'", "'unreachable'"} {
		if !strings.Contains(body, s) {
			t.Fatalf("RecoverExpired whitelist lost %s — regression of pre-existing recovery path", s)
		}
	}
}

// extractSnippet returns a 200-char window around the first occurrence
// of needle in body, for nicer test failure messages.
func extractSnippet(body, needle string) string {
	idx := strings.Index(body, needle)
	if idx < 0 {
		return "<needle not found>"
	}
	from := idx - 80
	to := idx + 200
	if from < 0 {
		from = 0
	}
	if to > len(body) {
		to = len(body)
	}
	return body[from:to]
}
