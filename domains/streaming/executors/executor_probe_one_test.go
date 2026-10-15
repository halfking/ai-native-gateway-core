package executors

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

func TestSelectProbeOneKeepsEarliestCoolUntil(t *testing.T) {
	now := time.Now().UTC()
	in := []credentialstate.NoCandidatesCandidate{
		{CredentialID: 30, RawModel: "m", CoolUntil: now.Add(30 * time.Minute)},
		{CredentialID: 10, RawModel: "m", CoolUntil: now.Add(5 * time.Minute)}, // soonest
		{CredentialID: 20, RawModel: "m", CoolUntil: now.Add(10 * time.Minute)},
	}
	got := selectProbeOne(in, true)
	if len(got) != 1 || got[0].CredentialID != 10 {
		t.Fatalf("selectProbeOne = %+v, want single cred 10 (earliest CoolUntil)", got)
	}
	// 零 CoolUntil 排最后：已知最早仍优先。
	in[1].CoolUntil = time.Time{}
	got = selectProbeOne(in, true)
	if len(got) != 1 || got[0].CredentialID != 20 {
		t.Fatalf("zero CoolUntil must sort last; got %+v, want cred 20", got)
	}
	// 前提在场：关闭时原样保留。
	if got := selectProbeOne(in, false); len(got) != 3 {
		t.Fatalf("disabled selectProbeOne len = %d, want 3", len(got))
	}
}

func TestSelectProbeOneFallsBackToCredentialIDWhenAllCoolUntilZero(t *testing.T) {
	in := []credentialstate.NoCandidatesCandidate{
		{CredentialID: 30, RawModel: "m"},
		{CredentialID: 10, RawModel: "m"},
		{CredentialID: 20, RawModel: "m"},
	}
	got := selectProbeOne(in, true)
	if len(got) != 1 || got[0].CredentialID != 10 {
		t.Fatalf("all-zero CoolUntil fallback = %+v, want cred 10", got)
	}
}

func TestCandidateCoolUntilFromRoutedNodeState(t *testing.T) {
	until := time.Now().Add(7 * time.Minute).UTC().Truncate(time.Second)
	c := provider.Candidate{
		CredentialID: 1,
		RoutedNodeState: &credentialfpslot.NodeState{
			DisabledUntil: until.Unix(),
		},
	}
	got := candidateCoolUntil(c)
	if !got.Equal(until) {
		t.Fatalf("candidateCoolUntil = %v, want %v", got, until)
	}
	if !candidateCoolUntil(provider.Candidate{CredentialID: 1}).IsZero() {
		t.Fatal("nil RoutedNodeState must yield zero CoolUntil")
	}
}

func TestRetryAfterSecondsFromEarliestReset(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if got := retryAfterSecondsFrom(time.Time{}, now); got != 30 {
		t.Fatalf("zero resetAt → %d, want 30", got)
	}
	if got := retryAfterSecondsFrom(now.Add(19*time.Minute), now); got != 19*60 {
		t.Fatalf("19min reset → %d, want %d", got, 19*60)
	}
	if got := retryAfterSecondsFrom(now.Add(3*time.Hour), now); got != 7200 {
		t.Fatalf("3h must clamp to 7200, got %d", got)
	}
}

func TestQuotaHeldAll(t *testing.T) {
	qs := "balance_exhausted"
	cands := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, QuotaState: qs, AvailabilityState: "ready", LifecycleStatus: "active", Routable: true},
		{CredentialID: 2, ProviderID: 1, QuotaState: "permanently_exhausted", AvailabilityState: "ready", LifecycleStatus: "active", Routable: true},
	}
	if !quotaHeldAll(cands) {
		t.Fatal("quotaHeldAll want true for all-quota candidates")
	}
	zero := 0.0
	cands[0].BalanceUSD = &zero
	if !quotaHeldAll(cands) {
		t.Fatal("quotaHeldAll want true for quota+balance:zero (semicolon-joined)")
	}
	// 生产形态：Routable=false → routing_blocked 包装仍算纯额度。
	cands[0].Routable = false
	cands[1].Routable = false
	if !quotaHeldAll(cands) {
		t.Fatal("quotaHeldAll want true when routing_blocked wraps quota: (production shape)")
	}
	cands[0].CircuitState = "open"
	if quotaHeldAll(cands) {
		t.Fatal("quotaHeldAll want false when circuit:open present")
	}
}

func TestToNoCandidatesProbeSliceSelectsEarliest(t *testing.T) {
	now := time.Now().UTC()
	e := &Executor{}
	cands := []provider.Candidate{
		{
			CredentialID: 3, ProviderID: 1, RawModel: "m",
			RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(30 * time.Minute).Unix()},
		},
		{
			CredentialID: 1, ProviderID: 1, RawModel: "m",
			RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(2 * time.Minute).Unix()},
		},
		{
			CredentialID: 2, ProviderID: 1, RawModel: "m",
			RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(10 * time.Minute).Unix()},
		},
	}
	got := e.toNoCandidatesProbeSlice(cands)
	if len(got) != 1 || got[0].CredentialID != 1 {
		t.Fatalf("toNoCandidatesProbeSlice = %+v, want single earliest cred 1", got)
	}
}

// TestDefaultProbeOneFlightIsPerExecutor 钉住：nil ProbeOneFlight 不得共用
// 进程级 map（否则 NoCandidateProbe_HonorsClientCancel 在同包先跑探测后会假红）。
func TestDefaultProbeOneFlightIsPerExecutor(t *testing.T) {
	a, b := &Executor{}, &Executor{}
	if !a.probeOneFlight().TryAcquire(context.Background(), "shared-model", time.Minute) {
		t.Fatal("executor A must acquire")
	}
	if !b.probeOneFlight().TryAcquire(context.Background(), "shared-model", time.Minute) {
		t.Fatal("executor B must acquire independently (per-Executor memory, not process-global)")
	}
	if a.probeOneFlight().TryAcquire(context.Background(), "shared-model", time.Minute) {
		t.Fatal("executor A second acquire must lose (same instance still holds)")
	}
}

func TestProbeOneFlightMemoryConcurrentAcquireOnce(t *testing.T) {
	flight := NewMemoryProbeOneFlight()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if flight.TryAcquire(context.Background(), "glm-5.3", time.Second) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent memory TryAcquire wins = %d, want 1", wins.Load())
	}
}

func TestProbeOneFlightRedisConcurrentAcquireOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	flight := NewRedisProbeOneFlight(rdb, "llmgw:probe_one:test:")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if flight.TryAcquire(context.Background(), "grok-4.6", time.Second) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent redis TryAcquire wins = %d, want 1", wins.Load())
	}
}

// TestProbeSyncCallCountUnderFlight 同层驱动生产入口：toNoCandidatesProbeSlice
// + ProbeOneFlight + ProbeSync 钩子。10 并发只触发 1 次上游探测；选中最早 CoolUntil。
func TestProbeSyncCallCountUnderFlight(t *testing.T) {
	now := time.Now().UTC()
	flight := NewMemoryProbeOneFlight()
	var probeCalls atomic.Int32
	var probedCred atomic.Int32
	e := &Executor{
		ProbeOneFlight: flight,
		ProbeSync: func(ctx context.Context, cands []credentialstate.NoCandidatesCandidate, tenantID, parentReqID string) bool {
			probeCalls.Add(1)
			if len(cands) != 1 {
				t.Errorf("ProbeSync cand len = %d, want 1", len(cands))
			} else {
				probedCred.Store(int32(cands[0].CredentialID))
			}
			time.Sleep(20 * time.Millisecond) // hold lock window for losers
			return false
		},
	}
	cands := []provider.Candidate{
		{CredentialID: 9, ProviderID: 1, RawModel: "m", RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(20 * time.Minute).Unix()}},
		{CredentialID: 4, ProviderID: 1, RawModel: "m", RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(1 * time.Minute).Unix()}},
		{CredentialID: 7, ProviderID: 1, RawModel: "m", RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(5 * time.Minute).Unix()}},
	}
	syncSlice := e.toNoCandidatesProbeSlice(cands)
	if len(syncSlice) != 1 || syncSlice[0].CredentialID != 4 {
		t.Fatalf("probe slice = %+v, want cred 4", syncSlice)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if e.probeOneFlight().TryAcquire(ctx, "test-model", time.Second) {
				_ = e.ProbeSync(ctx, syncSlice, "t", "r")
			}
		}()
	}
	wg.Wait()
	if probeCalls.Load() != 1 {
		t.Fatalf("ProbeSync calls = %d, want 1 (single-flight)", probeCalls.Load())
	}
	if probedCred.Load() != 4 {
		t.Fatalf("probed cred = %d, want 4 (earliest CoolUntil)", probedCred.Load())
	}
}

func TestEarliestCoolUntilDrivesRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cands := []provider.Candidate{
		{CredentialID: 1, RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(40 * time.Minute).Unix()}},
		{CredentialID: 2, RoutedNodeState: &credentialfpslot.NodeState{DisabledUntil: now.Add(19 * time.Minute).Unix()}},
	}
	earliest := earliestCoolUntil(cands, candidateCoolUntil)
	got := retryAfterSecondsFrom(earliest, now)
	if got != 19*60 {
		t.Fatalf("retryAfter from earliest = %d, want %d", got, 19*60)
	}
}
