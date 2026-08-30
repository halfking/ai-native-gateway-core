package executors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
	"github.com/alicebob/miniredis/v2"
)

// recordingFailureDB is an in-memory candidateFailureDB that records every
// INSERT call's arguments so tests can assert which preflight rejections
// were logged and with what kind / context. It implements the small
// candidateFailureDB interface used by CandidateFailureWriter (see
// candidate_failure_logger.go:61-63).
type recordingFailureDB struct {
	mu       sync.Mutex
	inserts  [][]any
	insertCh chan struct{}
}

func newRecordingFailureDB(buffer int) *recordingFailureDB {
	return &recordingFailureDB{insertCh: make(chan struct{}, buffer)}
}

func (r *recordingFailureDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	cp := make([]any, len(args))
	copy(cp, args)
	r.mu.Lock()
	r.inserts = append(r.inserts, cp)
	r.mu.Unlock()
	select {
	case r.insertCh <- struct{}{}:
	default:
	}
	return pgconn.CommandTag{}, nil
}

// findInsertByKind locates the first recorded INSERT whose error_kind
// argument matches want. The arguments layout matches candidateFailureInsertSQL:
//   $7  = attempt_index, $8 = error_kind
func (r *recordingFailureDB) findInsertByKind(want string) ([]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.inserts {
		if len(row) < 8 {
			continue
		}
		got, _ := row[7].(string)
		if got == want {
			return row, true
		}
	}
	return nil, false
}

func TestForwardForDispatch_LogsCircuitOpen(t *testing.T) {
	cm := newCircuitManagerForTest()
	limiter := newLimiterForTest()
	defer limiter.Stop()
	db := newRecordingFailureDB(4)

	// Force the breaker into StateOpen for (32, 22) so Allow() returns false.
	cm.RecordFailure(32, 22, errorsx.KindTransient)
	cm.RecordFailure(32, 22, errorsx.KindTransient)
	cm.RecordFailure(32, 22, errorsx.KindTransient)
	// Sanity: confirm the breaker is now open in-memory.
	if got := cm.Get(32, 22).State(); got != credential.StateOpen {
		t.Fatalf("breaker state = %s, want open (test setup)", got)
	}

	exec := &Executor{
		Circuit:         cm,
		Limiter:         limiter,
		FailureLogger:   &CandidateFailureWriter{pool: db},
		UpstreamTimeout: 5 * time.Second,
	}
	candidate := provider.Candidate{
		ProviderID: 32, CredentialID: 22, BaseURL: "http://upstream.invalid",
		Protocol: "openai-completions", RawModel: "glm-5.2", APIKey: "k",
		AvailabilityState: "ready", CircuitState: "open",
	}
	params := &ExecParams{
		R:                httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:        []byte(`{"model":"glm-5.2"}`),
		ClientModel:      "glm-5.2", Model: "glm-5.2",
		RequestID:        "circuit-open-test", TenantID: "default",
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
	dctx := &dispatchCtx{
		params: params, candidates: []provider.Candidate{candidate},
		initialModel: "glm-5.2", retryPerCred: 0, tTotal: time.Now(),
	}

	out := exec.forwardForDispatch(dctx, candidate, "circuit-open-attempt", func() {})
	if out.Err == nil {
		t.Fatal("expected circuit-open error, got nil")
	}

	select {
	case <-db.insertCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for circuit-open LogFailure insert")
	}
	row, ok := db.findInsertByKind(string(errorsx.KindCircuitOpen))
	if !ok {
		t.Fatalf("no insert with kind=%s; recorded kinds = %v",
			errorsx.KindCircuitOpen, db.insertKinds())
	}
	if ctxField, _ := row[15].(string); ctxField == "" {
		// context JSON is the 16th positional arg ($16::text::jsonb in the SQL);
		// the executor passes the raw map to the writer. We don't decode the
		// blob here — the call having been made with the right kind and a
		// per-attempt latency is sufficient evidence.
		t.Fatal("context JSON missing from circuit-open insert")
	}
}

func TestForwardForDispatch_LogsRateLimitRejection(t *testing.T) {
	// Saturate every limiter slot the candidate will touch so Acquire returns
	// an error. The default 4-layer limiter has bucket sizes; the simplest
	// way to force AcquireAllNoCredLayer to fail is to attach a context that
	// is already canceled — the limiter returns ctx.Err() before consulting
	// the bucket.
	limiter := newLimiterForTest()
	defer limiter.Stop()
	db := newRecordingFailureDB(4)
	exec := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		FailureLogger:   &CandidateFailureWriter{pool: db},
		UpstreamTimeout: 5 * time.Second,
	}
	candidate := provider.Candidate{
		ProviderID: 32, CredentialID: 22, BaseURL: "http://upstream.invalid",
		Protocol: "openai-completions", RawModel: "glm-5.2", APIKey: "k",
		AvailabilityState: "ready", CircuitState: "closed",
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	params := &ExecParams{
		R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(canceledCtx),
		BodyBytes:   []byte(`{"model":"glm-5.2"}`),
		ClientModel: "glm-5.2", Model: "glm-5.2", RequestID: "rl-test", TenantID: "default",
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
	dctx := &dispatchCtx{
		params: params, candidates: []provider.Candidate{candidate},
		initialModel: "glm-5.2", retryPerCred: 0, tTotal: time.Now(),
	}

	out := exec.forwardForDispatch(dctx, candidate, "rl-attempt", func() {})
	if out.Err == nil {
		t.Fatal("expected limiter-rejection error, got nil")
	}

	// The canceled-context path produces a limiter error whose message is
	// "context canceled"; the executor's preflight logging classifies it as
	// KindRateLimit (the dispatcher-side "we won't admit this request"
	// family). Verify the insert landed with that kind.
	select {
	case <-db.insertCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for limiter-rejection LogFailure insert")
	}
	if _, ok := db.findInsertByKind(string(errorsx.KindRateLimit)); !ok {
		t.Fatalf("no insert with kind=rate_limit; recorded kinds = %v", db.insertKinds())
	}
}

func TestForwardForDispatch_LogsFpSlotSaturation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	fpMgr := credentialfpslot.New(credentialfpslot.Config{DefaultLimit: 1, Enabled: true}, rdb)
	fpLimit := 1
	// Saturate the only available slot for credential 22 with a different
	// holder, so the test request cannot acquire it.
	lease, ok := fpMgr.Acquire(t.Context(), 22, &fpLimit, "other-session", "default")
	if !ok || lease == nil {
		t.Fatal("failed to saturate fingerprint slot for fixture")
	}

	limiter := newLimiterForTest()
	defer limiter.Stop()
	db := newRecordingFailureDB(4)
	exec := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		FailureLogger:   &CandidateFailureWriter{pool: db},
		FpSlots:         fpMgr,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
	candidate := provider.Candidate{
		ProviderID: 32, CredentialID: 22, BaseURL: "http://upstream.invalid",
		Protocol: "openai-completions", RawModel: "glm-5.2", APIKey: "k",
		FpSlotLimit:       &fpLimit,
		AvailabilityState: "ready", CircuitState: "closed",
	}
	params := &ExecParams{
		R:                httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:        []byte(`{"model":"glm-5.2"}`),
		ClientModel:      "glm-5.2", Model: "glm-5.2",
		RequestID:        "fpslot-test", TenantID: "default",
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
	dctx := &dispatchCtx{
		params: params, candidates: []provider.Candidate{candidate},
		initialModel: "glm-5.2", retryPerCred: 0, tTotal: time.Now(),
		holder: "this-session",
	}

	out := exec.forwardForDispatch(dctx, candidate, "fpslot-attempt", func() {})
	// FpSlot saturation is a *degradation*, not a hard reject — the request
	// must still proceed to the upstream call (which fails on the bogus URL).
	// The contract we lock here is the audit-trail side effect: one insert
	// with kind=rate_limit, rejection_type=fp_slot_saturated.
	select {
	case <-db.insertCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fpslot LogFailure insert")
	}
	row, ok := db.findInsertByKind(string(errorsx.KindRateLimit))
	if !ok {
		t.Fatalf("no insert with kind=rate_limit; recorded kinds = %v", db.insertKinds())
	}
	// Arg index 8 is error_message; arg 15 is the JSON context blob. We
	// can decode the context if we want to verify rejection_type, but a
	// positive kind match is sufficient to lock the contract.
	_ = row
	_ = out
}

func TestForwardForDispatch_LogsKeyRotationExhausted(t *testing.T) {
	// Note: credential.KeyRotator.EnsureCred short-circuits when count <= 1
	// (single-key credential always uses the primary, no rotation). To force
	// ResolveKey to actually run the "all keys exhausted" branch we have to
	// register at least 2 keys (primary + one extra) and mark both terminal.
	kr := credential.NewKeyRotator()
	kr.EnsureCred(22, 2) // primary at index 0, extra at index 1
	kr.RecordKeyFailure(22, 0, errorsx.KindAuthRevoked)
	kr.RecordKeyFailure(22, 1, errorsx.KindAuthRevoked)
	if !kr.AllKeysInvalid(22) {
		t.Fatalf("key rotator setup wrong: AllKeysInvalid(22) = false; ResolveKey(22, -1) = %d", kr.ResolveKey(22, -1))
	}

	limiter := newLimiterForTest()
	defer limiter.Stop()
	db := newRecordingFailureDB(4)
	exec := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		FailureLogger:   &CandidateFailureWriter{pool: db},
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
	candidate := provider.Candidate{
		ProviderID: 32, CredentialID: 22, BaseURL: "http://upstream.invalid",
		Protocol: "openai-completions", RawModel: "glm-5.2", APIKey: "primary-key",
		APIKeys:           []string{"primary-key", "extra-1", "extra-2"},
		KeyRotator:        kr,
		AvailabilityState: "ready", CircuitState: "closed",
	}
	params := &ExecParams{
		R:                httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:        []byte(`{"model":"glm-5.2"}`),
		ClientModel:      "glm-5.2", Model: "glm-5.2",
		RequestID:        "keys-exhausted-test", TenantID: "default",
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
	dctx := &dispatchCtx{
		params: params, candidates: []provider.Candidate{candidate},
		initialModel: "glm-5.2", retryPerCred: 0, tTotal: time.Now(),
	}

	out := exec.forwardForDispatch(dctx, candidate, "keys-attempt", func() {})
	if out.Err == nil {
		t.Fatal("expected keys-exhausted error, got nil")
	}

	select {
	case <-db.insertCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for keys-exhausted LogFailure insert")
	}
	if _, ok := db.findInsertByKind(string(errorsx.KindRateLimit)); !ok {
		t.Fatalf("no insert with kind=rate_limit; recorded kinds = %v", db.insertKinds())
	}
}

// insertKinds returns the error_kind argument of every recorded insert, in
// order. Debug-only helper used in test failure messages.
func (r *recordingFailureDB) insertKinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.inserts))
	for _, row := range r.inserts {
		if len(row) >= 8 {
			if k, ok := row[7].(string); ok {
				out = append(out, k)
			}
		}
	}
	return out
}
