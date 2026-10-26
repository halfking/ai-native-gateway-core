package executors

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

// P0-D 同层驱动 Execute 生产入口（非手搓 flight 旁路）——规划判据：
// 3 候选全冷却、到期不同 → ProbeSync 只命中最早到期者 1 次；
// 10 并发 → ProbeSync 调用 = 1；全 quota → 上游探测 = 0 且 RetryAfter 对齐。

func TestExecuteProbeOneSelectsEarliestCoolUntil(t *testing.T) {
	now := time.Now().UTC()
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	flight := NewMemoryProbeOneFlight()

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 NewRouter(NewStickyCache(), lim),
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 2 * time.Second,
		ProbeOneFlight:         flight,
	}
	wireDispatchPipelineForTest(t, e)

	var probeCalls atomic.Int32
	var probedCred atomic.Int32
	e.ProbeSync = func(ctx context.Context, cands []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		probeCalls.Add(1)
		if len(cands) != 1 {
			t.Errorf("ProbeSync len=%d, want 1", len(cands))
		} else {
			probedCred.Store(int32(cands[0].CredentialID))
		}
		return false
	}

	cands := []provider.Candidate{
		cooledCand(9, now.Add(20*time.Minute)),
		cooledCand(4, now.Add(1*time.Minute)), // earliest
		cooledCand(7, now.Add(5*time.Minute)),
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	rec := httptest.NewRecorder()
	_, err := e.Execute(&ExecParams{
		W: rec, R: r,
		BodyBytes:     []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		ClientModel:   "m",
		OutboundModel: "m",
		Candidates:    cands,
		StickyKey:     "p0d-earliest",
		Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:     "p0d-earliest",
	})
	if err == nil {
		t.Fatal("Execute want error when probe does not recover")
	}
	if probeCalls.Load() != 1 {
		t.Fatalf("ProbeSync calls=%d, want 1", probeCalls.Load())
	}
	if probedCred.Load() != 4 {
		t.Fatalf("probed cred=%d, want 4 (earliest CoolUntil)", probedCred.Load())
	}
}

func TestExecuteProbeOneSingleFlightConcurrent(t *testing.T) {
	now := time.Now().UTC()
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	flight := NewMemoryProbeOneFlight()

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 NewRouter(NewStickyCache(), lim),
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 2 * time.Second,
		ProbeOneFlight:         flight,
	}
	wireDispatchPipelineForTest(t, e)

	var probeCalls atomic.Int32
	e.ProbeSync = func(ctx context.Context, cands []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		probeCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return false
	}

	cands := []provider.Candidate{
		cooledCand(1, now.Add(2*time.Minute)),
		cooledCand(2, now.Add(10*time.Minute)),
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
			rec := httptest.NewRecorder()
			_, _ = e.Execute(&ExecParams{
				W: rec, R: r,
				BodyBytes:     []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
				ClientModel:   "m", // same model → same flight key
				OutboundModel: "m",
				Candidates:    cands,
				StickyKey:     "p0d-flight",
				Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
				RequestID:     "p0d-flight-" + string(rune('a'+i)),
			})
		}(i)
	}
	wg.Wait()
	if probeCalls.Load() != 1 {
		t.Fatalf("concurrent Execute ProbeSync calls=%d, want 1 (single-flight)", probeCalls.Load())
	}
}

// TestExecuteProbeOneRedisSingleFlightConcurrent：生产 Redis SET NX 腿经
// Execute 同层——miniredis 10 并发，ProbeSync 仍 = 1（非旁路 TryAcquire）。
func TestExecuteProbeOneRedisSingleFlightConcurrent(t *testing.T) {
	now := time.Now().UTC()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 NewRouter(NewStickyCache(), lim),
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 2 * time.Second,
		ProbeOneFlight:         NewRedisProbeOneFlight(rdb, "llmgw:probe_one:p0d:"),
	}
	wireDispatchPipelineForTest(t, e)

	var probeCalls atomic.Int32
	e.ProbeSync = func(ctx context.Context, cands []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		probeCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return false
	}

	cands := []provider.Candidate{
		cooledCand(1, now.Add(2*time.Minute)),
		cooledCand(2, now.Add(10*time.Minute)),
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
			rec := httptest.NewRecorder()
			_, _ = e.Execute(&ExecParams{
				W: rec, R: r,
				BodyBytes:     []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
				ClientModel:   "m",
				OutboundModel: "m",
				Candidates:    cands,
				StickyKey:     "p0d-redis-flight",
				Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
				RequestID:     "p0d-redis-flight-" + string(rune('a'+i)),
			})
		}(i)
	}
	wg.Wait()
	if probeCalls.Load() != 1 {
		t.Fatalf("redis Execute ProbeSync calls=%d, want 1 (SET NX single-flight)", probeCalls.Load())
	}
}

func TestExecuteSurvivalQuotaHeldSkipsProbeAndSetsRetryAfter(t *testing.T) {
	now := time.Now().UTC()
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 NewRouter(NewStickyCache(), lim),
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 2 * time.Second,
		ProbeOneFlight:         NewMemoryProbeOneFlight(),
	}
	wireDispatchPipelineForTest(t, e)

	var probeCalls atomic.Int32
	e.ProbeSync = func(context.Context, []credentialstate.NoCandidatesCandidate, string, string) bool {
		probeCalls.Add(1)
		return false
	}

	cands := []provider.Candidate{
		quotaHeldCand(1, now.Add(40*time.Minute)),
		quotaHeldCand(2, now.Add(19*time.Minute)),
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	rec := httptest.NewRecorder()
	_, err := e.Execute(&ExecParams{
		W: rec, R: r,
		BodyBytes:       []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		ClientModel:     "m",
		OutboundModel:   "m",
		Candidates:      cands,
		StickyKey:       "p0d-survival-quota",
		Policy:          &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:       "p0d-survival-quota",
		SurvivalAttempt: true,
	})
	if err == nil {
		t.Fatal("survival Execute want quotaHeld error")
	}
	var ee *ExecuteError
	if !errors.As(err, &ee) {
		t.Fatalf("err type %T, want *ExecuteError", err)
	}
	if ee.Tried != 0 || ee.LastKind != errorsx.KindQuota {
		t.Fatalf("ExecuteError tried=%d kind=%s, want Tried=0 KindQuota", ee.Tried, ee.LastKind)
	}
	if probeCalls.Load() != 0 {
		t.Fatalf("ProbeSync calls=%d, want 0 (quotaHeld survival)", probeCalls.Load())
	}
	if ee.RetryAfterSeconds < 19*60-5 || ee.RetryAfterSeconds > 19*60+5 {
		t.Fatalf("RetryAfterSeconds=%d, want ≈%d", ee.RetryAfterSeconds, 19*60)
	}
}

func TestExecuteSurvivalProbeOneRecoversWithoutRecursiveExecute(t *testing.T) {
	now := time.Now().UTC()
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()
	flight := NewMemoryProbeOneFlight()

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 NewRouter(NewStickyCache(), lim),
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 2 * time.Second,
		ProbeOneFlight:         flight,
	}
	wireDispatchPipelineForTest(t, e)

	var probeCalls atomic.Int32
	var probedCred atomic.Int32
	e.ProbeSync = func(_ context.Context, cands []credentialstate.NoCandidatesCandidate, _, _ string) bool {
		probeCalls.Add(1)
		if len(cands) != 1 {
			t.Errorf("ProbeSync candidates=%d, want 1", len(cands))
		} else {
			probedCred.Store(int32(cands[0].CredentialID))
		}
		return true
	}

	cands := []provider.Candidate{
		cooledCand(9, now.Add(20*time.Minute)),
		cooledCand(4, now.Add(1*time.Minute)),
		cooledCand(7, now.Add(5*time.Minute)),
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	rec := httptest.NewRecorder()
	_, err := e.Execute(&ExecParams{
		W: rec, R: r,
		BodyBytes:       []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		ClientModel:     "m",
		OutboundModel:   "m",
		Candidates:      cands,
		StickyKey:       "p0d-survival-recover",
		Policy:          &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:       "p0d-survival-recover",
		SurvivalAttempt: true,
	})
	if err == nil {
		t.Fatal("survival Execute must return after probe recovery instead of recursing")
	}
	var ee *ExecuteError
	if !errors.As(err, &ee) {
		t.Fatalf("err type %T, want *ExecuteError", err)
	}
	if ee.Tried != 0 || !ee.Exhausted || ee.LastKind != errorsx.KindTransient {
		t.Fatalf("survival recovery result = %+v, want bounded transient/no-candidate outcome", ee)
	}
	if probeCalls.Load() != 1 || probedCred.Load() != 4 {
		t.Fatalf("ProbeSync calls=%d cred=%d, want one earliest-cred=4 probe", probeCalls.Load(), probedCred.Load())
	}
}

func TestExecuteQuotaHeldSkipsProbeAndSetsRetryAfter(t *testing.T) {
	now := time.Now().UTC()
	cm := newCircuitManagerForTest()
	lim := newLimiterForTest()
	defer lim.Stop()

	e := &Executor{
		Circuit:                cm,
		Limiter:                lim,
		Router:                 NewRouter(NewStickyCache(), lim),
		UpstreamTimeout:        5 * time.Second,
		StreamTimeout:          10 * time.Second,
		SyncNoCandidateProbe:   true,
		SyncNoCandidateTimeout: 2 * time.Second,
		ProbeOneFlight:         NewMemoryProbeOneFlight(),
	}
	wireDispatchPipelineForTest(t, e)

	var probeCalls atomic.Int32
	e.ProbeSync = func(ctx context.Context, cands []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
		probeCalls.Add(1)
		return false
	}

	// 生产形态：Routable=false + quota + CoolUntil（earliest = 19min）。
	cands := []provider.Candidate{
		quotaHeldCand(1, now.Add(40*time.Minute)),
		quotaHeldCand(2, now.Add(19*time.Minute)),
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	rec := httptest.NewRecorder()
	_, err := e.Execute(&ExecParams{
		W: rec, R: r,
		BodyBytes:     []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		ClientModel:   "m",
		OutboundModel: "m",
		Candidates:    cands,
		StickyKey:     "p0d-quota",
		Policy:        &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		RequestID:     "p0d-quota",
	})
	if err == nil {
		t.Fatal("Execute want quotaHeld error")
	}
	var ee *ExecuteError
	if !errors.As(err, &ee) {
		t.Fatalf("err type %T, want *ExecuteError", err)
	}
	if ee.Tried != 0 || ee.LastKind != errorsx.KindQuota {
		t.Fatalf("ExecuteError tried=%d kind=%s, want Tried=0 KindQuota", ee.Tried, ee.LastKind)
	}
	if probeCalls.Load() != 0 {
		t.Fatalf("ProbeSync calls=%d, want 0 (quotaHeld)", probeCalls.Load())
	}
	// Retry-After ≈ 19min（允许数秒时钟偏斜）；handler 经 RetryAfterSeconds hint 透传。
	if ee.RetryAfterSeconds < 19*60-5 || ee.RetryAfterSeconds > 19*60+5 {
		t.Fatalf("RetryAfterSeconds=%d, want ≈%d", ee.RetryAfterSeconds, 19*60)
	}
}

func cooledCand(credID int, until time.Time) provider.Candidate {
	return provider.Candidate{
		ProviderID:        1,
		CredentialID:      credID,
		BaseURL:           "http://127.0.0.1:1",
		Protocol:          "openai-completions",
		APIKey:            "sk-test",
		RawModel:          "m",
		Tier:              2,
		BillingMode:       "token",
		Routable:          false,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		RoutedNodeState: &credentialfpslot.NodeState{
			DisabledUntil: until.Unix(),
		},
	}
}

func quotaHeldCand(credID int, until time.Time) provider.Candidate {
	c := cooledCand(credID, until)
	c.QuotaState = "balance_exhausted"
	return c
}
