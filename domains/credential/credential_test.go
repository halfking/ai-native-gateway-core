package credential

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// InMemoryStore (unchanged from pre-migration)
// ---------------------------------------------------------------------------

func TestInMemoryStore_SaveAndGet(t *testing.T) {
	s := NewInMemoryStore()
	cred := &Credential{
		ID: "c1", TenantID: "t1", ProviderID: "openai", Model: "gpt-4",
		EncryptedKey: []byte("encrypted"), MaxConcurrent: 10, Priority: 50,
		Status: StatusActive,
	}
	if err := s.Save(cred); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	got, ok, err := s.Get("c1")
	if err != nil || !ok {
		t.Fatalf("Get failed: ok=%v err=%v", ok, err)
	}
	if got.TenantID != "t1" {
		t.Errorf("expected tenant t1, got %q", got.TenantID)
	}
}

func TestInMemoryStore_GetNotFound(t *testing.T) {
	s := NewInMemoryStore()
	_, ok, err := s.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get should not error: %v", err)
	}
	if ok {
		t.Error("expected ok=false")
	}
}

func TestInMemoryStore_GetReturnsCopy(t *testing.T) {
	s := NewInMemoryStore()
	cred := &Credential{ID: "c1", TenantID: "t1", Metadata: map[string]any{"k": "v"}}
	_ = s.Save(cred)
	got, _, _ := s.Get("c1")
	got.Metadata["k"] = "modified"
	original, _, _ := s.Get("c1")
	if original.Metadata["k"] != "v" {
		t.Error("Get should return copy")
	}
}

func TestInMemoryStore_Delete(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1"})
	_ = s.Delete("c1")
	if s.Count() != 0 {
		t.Errorf("expected 0, got %d", s.Count())
	}
}

func TestInMemoryStore_ListByTenant(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1"})
	_ = s.Save(&Credential{ID: "c2", TenantID: "t1"})
	_ = s.Save(&Credential{ID: "c3", TenantID: "t2"})
	list, _ := s.List("t1")
	if len(list) != 2 {
		t.Errorf("expected 2 credentials for t1, got %d", len(list))
	}
}

func TestInMemoryStore_SaveRequiresID(t *testing.T) {
	s := NewInMemoryStore()
	if err := s.Save(&Credential{}); err == nil {
		t.Error("expected error for empty ID")
	}
	if err := s.Save(nil); err == nil {
		t.Error("expected error for nil")
	}
}

// ---------------------------------------------------------------------------
// HealthChecker (unchanged from pre-migration)
// ---------------------------------------------------------------------------

func TestHealthChecker_MarkSuccess(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	_ = s.Save(&Credential{ID: "c1", Status: StatusDegraded, ConsecutiveFails: 1})
	if err := hc.MarkSuccess("c1"); err != nil {
		t.Fatalf("MarkSuccess failed: %v", err)
	}
	cred, _, _ := s.Get("c1")
	if cred.Status != StatusActive {
		t.Errorf("expected StatusActive, got %q", cred.Status)
	}
	if cred.ConsecutiveFails != 0 {
		t.Errorf("expected 0 fails, got %d", cred.ConsecutiveFails)
	}
}

func TestHealthChecker_MarkFailure(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	hc.failThreshold = 2
	_ = s.Save(&Credential{ID: "c1", Status: StatusActive})

	_ = hc.MarkFailure("c1")
	cred, _, _ := s.Get("c1")
	if cred.Status != StatusDegraded {
		t.Errorf("expected StatusDegraded, got %q", cred.Status)
	}

	_ = hc.MarkFailure("c1")
	cred, _, _ = s.Get("c1")
	if cred.Status != StatusUnhealthy {
		t.Errorf("expected StatusUnhealthy, got %q", cred.Status)
	}
}

func TestHealthChecker_FilterHealthy(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	creds := []*Credential{
		{ID: "c1", Status: StatusActive},
		{ID: "c2", Status: StatusUnhealthy},
		{ID: "c3", Status: StatusActive},
	}
	healthy := hc.FilterHealthy(creds)
	if len(healthy) != 2 {
		t.Errorf("expected 2 healthy, got %d", len(healthy))
	}
}

func TestHealthChecker_MarkRequiresID(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	if err := hc.MarkSuccess(""); err == nil {
		t.Error("expected error for empty ID")
	}
}

// ---------------------------------------------------------------------------
// Limiter — migrated 4-layer concurrency controller
// (replaces pre-migration 63-line simplified interface; full coverage
// lives in limiter_test.go and limiter_concurrent_test.go)
// ---------------------------------------------------------------------------

func TestLimiter_NewAndStop(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	if l.Global().Capacity() != DefaultGlobalLimit {
		t.Errorf("expected global %d, got %d", DefaultGlobalLimit, l.Global().Capacity())
	}
}

func TestLimiter_CredentialSemaphore(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	s1 := l.Credential(1, 1)
	s2 := l.Credential(1, 1)
	if s1 != s2 {
		t.Error("Credential should return same instance for same (provider, credential)")
	}
	s3 := l.Credential(1, 2)
	if s1 == s3 {
		t.Error("Credential should return different instance for different credential")
	}
}

// ---------------------------------------------------------------------------
// Breaker — migrated mature circuit breaker with 4 states (closed/open/
// half_open/quarantined) and errorsx classification.
// (Full coverage lives in breaker_test.go — 413 lines, 19+ tests.)
// ---------------------------------------------------------------------------

func TestBreaker_QuarantinedStateExists(t *testing.T) {
	// Pin the existence of StateQuarantined (it was lost in the pre-migration
	// 98-line simplified breaker and is one of the must-keep invariants of
	// the 2026-06-26 migration).
	b := New(1, 1)
	b.RecordFailure(errorsx.KindAuth)
	b.RecordFailure(errorsx.KindAuth)
	if b.State() != StateQuarantined {
		t.Fatalf("expected StateQuarantined after 2 auth failures, got %s", b.State())
	}
	if b.Allow() {
		t.Error("quarantined breaker must not allow")
	}
}

// ---------------------------------------------------------------------------
// LimiterHook — uses the migrated 4-layer Limiter via the per-credential
// layer (Limiter.Credential). OnError releases the same slot.
// ---------------------------------------------------------------------------

func TestLimiterHook_AcquiresAndReleases(t *testing.T) {
	// credential limit = 1 so the 2nd acquire fails immediately
	l := NewWithLimits(100, 100, 1, 100)
	defer l.Stop()
	hook := NewLimiterHook(l)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.SelectedCredential = &domain.PipelineCredential{ID: "c1"}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if locked, _ := env.Metadata["credential_locked"].(bool); !locked {
		t.Error("expected credential_locked=true")
	}

	// 2nd request with same credID hashes to the same credential slot →
	// credential limit (1) is reached → should be rate-limited.
	env2 := domain.NewRequestEnvelope(context.Background(), nil)
	env2.SelectedCredential = &domain.PipelineCredential{ID: "c1"}
	if err := hook.Execute(context.Background(), env2); err == nil {
		t.Error("2nd request should be rate-limited")
	}

	// OnError releases the slot.
	_ = hook.OnError(context.Background(), env, errors.New("oops"))

	// 3rd request should now succeed.
	env3 := domain.NewRequestEnvelope(context.Background(), nil)
	env3.SelectedCredential = &domain.PipelineCredential{ID: "c1"}
	if err := hook.Execute(context.Background(), env3); err != nil {
		t.Errorf("3rd request after release should succeed, got %v", err)
	}
}

func TestLimiterHook_NoSelectedCredential(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	hook := NewLimiterHook(l)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := hook.Execute(context.Background(), env)
	if err == nil {
		t.Error("expected error when no SelectedCredential")
	}
}

// ---------------------------------------------------------------------------
// HealthCheckHook (unchanged from pre-migration)
// ---------------------------------------------------------------------------

func TestHealthCheckHook_LoadsCandidates(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1", ProviderID: "p1", Status: StatusActive})
	_ = s.Save(&Credential{ID: "c2", TenantID: "t1", ProviderID: "p2", Status: StatusUnhealthy})

	hook := NewHealthCheckHook(s, hc)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "t1"
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	count, _ := env.Metadata["credential_count"].(int)
	if count != 1 {
		t.Errorf("expected 1 healthy credential, got %d", count)
	}
}

func TestHealthCheckHook_DifferentTenants(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1", Status: StatusActive})
	_ = s.Save(&Credential{ID: "c2", TenantID: "t2", Status: StatusActive})

	hook := NewHealthCheckHook(s, hc)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "t1"
	_ = hook.Execute(context.Background(), env)
	count, _ := env.Metadata["credential_count"].(int)
	if count != 1 {
		t.Errorf("expected 1 credential for t1, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Crypto (unchanged from pre-migration)
// ---------------------------------------------------------------------------

func TestPlainCrypto_Roundtrip(t *testing.T) {
	c := PlainCrypto{}
	enc, _ := c.Encrypt([]byte("secret"))
	dec, _ := c.Decrypt(enc)
	if string(dec) != "secret" {
		t.Errorf("expected 'secret', got %q", string(dec))
	}
}

// ---------------------------------------------------------------------------
// Design intent — pin the architectural decisions of the 2026-06-26 migration.
// ---------------------------------------------------------------------------

func TestDesignIntent_PipelineHookStoresSemaphoreInMetadata(t *testing.T) {
	// The hook stores the per-credential *Semaphore in env.Metadata so that
	// OnError can release the exact same slot. If anyone reverts to a
	// release-by-id approach, the concurrency invariant breaks (different
	// requests to the same credential would release each other's slots).
	l := NewWithLimits(100, 100, 100, 100)
	defer l.Stop()
	hook := NewLimiterHook(l)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.SelectedCredential = &domain.PipelineCredential{ID: "sem-test"}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := env.Metadata["credential_semaphore"].(*Semaphore); !ok {
		t.Fatal("expected *Semaphore stored in env.Metadata[\"credential_semaphore\"]")
	}
}

func TestDesignIntent_FNVHashIsStable(t *testing.T) {
	// fnvHash maps string credential IDs to int credential slots. It MUST
	// be stable across calls — otherwise the same credential could land on
	// different semaphore slots and the rate limit would be ineffective.
	if fnvHash("c1") != fnvHash("c1") { //nolint:staticcheck // SA4000: intentionally testing self-equality (asserting determinism)
		t.Fatal("fnvHash must be deterministic")
	}
	if fnvHash("c1") == fnvHash("c2") {
		t.Fatal("fnvHash should not collide on common IDs (c1 vs c2)")
	}
}

// ---------------------------------------------------------------------------
// Coverage boost — cover trivially-untested one-liners on the migrated types
// ---------------------------------------------------------------------------

func TestBreaker_Key(t *testing.T) {
	b := New(7, 11)
	if got := b.Key(); got != "7/11" {
		t.Errorf("expected key \"7/11\", got %q", got)
	}
}

func TestState_String(t *testing.T) {
	cases := []struct {
		s    State
		want string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half_open"},
		{StateQuarantined, "quarantined"},
		{State(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("State(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestHealthCheckHook_Metadata(t *testing.T) {
	// Cover Name, Priority, Enabled (the trivially-untested hook methods).
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	hook := NewHealthCheckHook(s, hc)

	if hook.Name() != "credential.health" {
		t.Errorf("unexpected name: %q", hook.Name())
	}
	if hook.Priority() != 50 {
		t.Errorf("unexpected priority: %d", hook.Priority())
	}
	env := &domain.PipelineRequest{Metadata: map[string]any{}}
	if !hook.Enabled(context.Background(), env) {
		t.Error("expected enabled for non-nil env")
	}
	if hook.Enabled(context.Background(), nil) {
		t.Error("expected disabled for nil env")
	}
	// OnError of HealthCheckHook is a pass-through that returns the error.
	if err := hook.OnError(context.Background(), env, errors.New("boom")); err == nil || err.Error() != "boom" {
		t.Errorf("OnError pass-through failed: %v", err)
	}
}

func TestLimiterHook_Metadata(t *testing.T) {
	// Cover Name, Priority, Enabled for the migrated LimiterHook.
	l := NewLimiter()
	defer l.Stop()
	hook := NewLimiterHook(l)

	if hook.Name() != "credential.limit" {
		t.Errorf("unexpected name: %q", hook.Name())
	}
	if hook.Priority() != 150 {
		t.Errorf("unexpected priority: %d", hook.Priority())
	}
	envNil := &domain.PipelineRequest{Metadata: map[string]any{}}
	if hook.Enabled(context.Background(), envNil) {
		t.Error("expected disabled when SelectedCredential is nil")
	}
	// OnError with nil env must not panic.
	if err := hook.OnError(context.Background(), nil, errors.New("x")); err == nil || err.Error() != "x" {
		t.Errorf("OnError nil-env should pass through err, got %v", err)
	}
}

func TestRedisIdentityLimiter_DisabledPath(t *testing.T) {
	// Redis is not enabled (nil client) — every method must return its
	// not-enabled sentinel instead of panicking on a nil deref. This is
	// the path taken by the data plane when REDIS_URL is unset.
	r := &RedisIdentityLimiter{client: nil}
	if r.Enabled() {
		t.Fatal("nil client should report Enabled()=false")
	}
	_, err := r.Acquire(context.Background(), 1, 1, "hash", 10)
	if err == nil {
		t.Fatal("expected error when redis is disabled")
	}
	// Release/Stats are no-ops on the disabled path.
	if err := r.Release(context.Background(), 1, 1, "hash"); err != nil {
		t.Errorf("Release on disabled limiter should be no-op, got %v", err)
	}
	if used, err := r.Stats(context.Background(), 1, 1, "hash"); err != nil || used != 0 {
		t.Errorf("Stats on disabled limiter should return (0, nil), got (%d, %v)", used, err)
	}
	if got := truncateHash("abcdefghij"); got != "abcdefgh" {
		t.Errorf("truncateHash should cap at 8 chars, got %q", got)
	}
}

func TestWriter_NewWriter_EnabledAndErrors(t *testing.T) {
	// newWriterWithDB / NewWriter can only be exercised via a real *pgxpool.Pool
	// or pgxmock. We pin Enabled()=false for a nil writer to cover the
	// "db not configured" guard.
	var w *Writer
	if w.Enabled() {
		t.Error("nil writer should report Enabled()=false")
	}
	// All Writer methods on a nil writer return ErrNoDatabase.
	if err := w.RestoreOnSuccess(context.Background(), 1, ""); err != ErrNoDatabase {
		t.Errorf("nil RestoreOnSuccess: want ErrNoDatabase, got %v", err)
	}
	if err := w.WriteOnError(context.Background(), 1, "m", Failure{Kind: errorsx.KindNetwork}); err != ErrNoDatabase {
		t.Errorf("nil WriteOnError: want ErrNoDatabase, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RedisIdentityLimiter — backed by miniredis to exercise Acquire / Release /
// Stats against the actual Lua scripts. miniredis is an indirect dep that
// ships with the repo, so we use it instead of a real Redis.
// ---------------------------------------------------------------------------

func newMiniredisClient(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return mr, c
}

func TestRedisIdentityLimiter_NewAndEnabled(t *testing.T) {
	_, c := newMiniredisClient(t)
	r := NewRedisIdentityLimiter(c)
	if !r.Enabled() {
		t.Error("expected Enabled()=true with non-nil client")
	}
	// identityKey is deterministic — same inputs → same key.
	k1 := r.identityKey(1, 2, "abc")
	k2 := r.identityKey(1, 2, "abc")
	if k1 != k2 {
		t.Errorf("identityKey not stable: %q vs %q", k1, k2)
	}
	if r.identityKey(1, 2, "abc") == r.identityKey(1, 2, "abd") {
		t.Error("identityKey should differ for different hashes")
	}
	if r.identityKey(1, 2, "h") == r.identityKey(1, 3, "h") {
		t.Error("identityKey should differ for different credentials")
	}
}

func TestRedisIdentityLimiter_AcquireReleaseStats(t *testing.T) {
	_, c := newMiniredisClient(t)
	r := NewRedisIdentityLimiter(c)
	ctx := context.Background()

	ok, err := r.Acquire(ctx, 1, 1, "hash1", 3)
	if err != nil || !ok {
		t.Fatalf("1st Acquire: ok=%v err=%v", ok, err)
	}
	ok, err = r.Acquire(ctx, 1, 1, "hash1", 3)
	if err != nil || !ok {
		t.Fatalf("2nd Acquire: ok=%v err=%v", ok, err)
	}
	ok, err = r.Acquire(ctx, 1, 1, "hash1", 3)
	if err != nil || !ok {
		t.Fatalf("3rd Acquire: ok=%v err=%v", ok, err)
	}
	// 4th acquire should be rejected — limit is 3.
	ok, err = r.Acquire(ctx, 1, 1, "hash1", 3)
	if err != nil {
		t.Fatalf("4th Acquire error: %v", err)
	}
	if ok {
		t.Error("4th Acquire should be rejected (limit=3)")
	}

	used, err := r.Stats(ctx, 1, 1, "hash1")
	if err != nil || used != 3 {
		t.Errorf("Stats: used=%d err=%v", used, err)
	}

	if err := r.Release(ctx, 1, 1, "hash1"); err != nil {
		t.Errorf("Release: %v", err)
	}
	used, _ = r.Stats(ctx, 1, 1, "hash1")
	if used != 2 {
		t.Errorf("Stats after Release: used=%d want 2", used)
	}

	// Now a new Acquire should succeed.
	ok, err = r.Acquire(ctx, 1, 1, "hash1", 3)
	if err != nil || !ok {
		t.Errorf("Acquire after Release: ok=%v err=%v", ok, err)
	}
}

func TestRedisIdentityLimiter_StatsEmptyKey(t *testing.T) {
	_, c := newMiniredisClient(t)
	r := NewRedisIdentityLimiter(c)
	used, err := r.Stats(context.Background(), 99, 99, "never-set")
	if err != nil || used != 0 {
		t.Errorf("Stats for missing key: used=%d err=%v", used, err)
	}
}

func TestRedisIdentityLimiter_TruncateHashEdgeCases(t *testing.T) {
	if got := truncateHash(""); got != "" {
		t.Errorf("empty hash: %q", got)
	}
	if got := truncateHash("short"); got != "short" {
		t.Errorf("short hash: %q", got)
	}
	if got := truncateHash("12345678"); got != "12345678" {
		t.Errorf("8-char hash should pass through: %q", got)
	}
	if got := truncateHash("123456789"); got != "12345678" {
		t.Errorf("9-char hash should be truncated: %q", got)
	}
}
