package executors

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/shadow"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func waitForShadowCount(t *testing.T, outcome shadow.Outcome, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if shadow.Snapshot()[outcome] >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("shadow counter %q=%d, want at least %d", outcome, shadow.Snapshot()[outcome], want)
}

func TestURSMShadowTaskContainsOnlySeedsAndIDs(t *testing.T) {
	secret := "sk-secret"
	candidate := provider.Candidate{
		ProviderID: 10, CredentialID: 1, RawModel: "m", StandardizedName: "model",
		APIKey: secret, APIKeys: []string{secret}, BaseURL: "https://secret.example",
	}
	task := ursmShadowTask{
		seeds:     []ursmv2.CandidateSeed{candidateSeed(candidate, "tenant", "model")},
		legacyIDs: []string{seedLookupKey(candidate.ProviderID, candidate.CredentialID, candidate.RawModel)},
	}
	if got := task.seeds[0]; got.ProviderID != 10 || got.CredentialID != 1 || got.RawModel != "m" {
		t.Fatalf("unexpected redacted seed: %+v", got)
	}
	if task.legacyIDs[0] != "10|1|m" {
		t.Fatalf("unexpected legacy id: %q", task.legacyIDs[0])
	}
}

func TestURSMShadowWorkerQueueFullAndStop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	worker := newURSMShadowWorker(1, func(ursmShadowTask) {
		once.Do(func() { close(started) })
		<-release
	})

	if !worker.enqueue(ursmShadowTask{}) {
		t.Fatal("first task should start")
	}
	<-started
	if !worker.enqueue(ursmShadowTask{}) {
		t.Fatal("second task should fill the bounded queue")
	}
	if worker.enqueue(ursmShadowTask{}) {
		t.Fatal("third task must be dropped when queue is full")
	}

	stopped := make(chan struct{})
	go func() {
		worker.stopAndWait()
		close(stopped)
	}()
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if worker.enqueue(ursmShadowTask{}) {
		t.Fatal("stopped worker must reject new tasks")
	}
}

func TestRouterShadowQueueFullRecordsDrop(t *testing.T) {
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = ursmv2api.ModeShadow
	cfg.ShadowSampleRate = 1
	mgr := ursmv2.New(ursmv2.Dependencies{Config: cfg})
	t.Cleanup(mgr.Close)

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	worker := newURSMShadowWorker(1, func(ursmShadowTask) {
		once.Do(func() { close(started) })
		<-release
	})
	router := NewRouter(nil, nil)
	router.URSMv2 = mgr
	router.shadowWorker = worker
	candidate := []provider.Candidate{{ProviderID: 10, CredentialID: 1, RawModel: "m"}}

	router.enqueueURSMv2Shadow(candidate, candidate, "tenant", "m", "request-1")
	<-started
	router.enqueueURSMv2Shadow(candidate, candidate, "tenant", "m", "request-2")
	before := shadow.Snapshot()[shadow.OutcomeDropped]
	router.enqueueURSMv2Shadow(candidate, candidate, "tenant", "m", "request-3")
	waitForShadowCount(t, shadow.OutcomeDropped, before+1)

	close(release)
	router.StopShadowWorker()
}

func TestRouterShadowDiffDoesNotChangeLegacyOrder(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = ursmv2api.ModeShadow
	cfg.ShadowSampleRate = 1
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	seed := ursmv2.CandidateSeed{ProviderID: 10, CredentialID: 1, RawModel: "m", TenantID: "tenant"}
	if err := mgr.SetSeedForTest(context.Background(), seed); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	router := NewRouter(nil, nil)
	t.Cleanup(router.StopShadowWorker)
	router.URSMv2 = mgr
	candidates := []provider.Candidate{{ProviderID: 10, CredentialID: 1, RawModel: "m", Tier: 1, Routable: true}}
	before := shadow.Snapshot()[shadow.OutcomeIdentical]
	got := router.PlanCandidatesWithContext(context.Background(), candidates, nil, &provider.Policy{}, nil, "tenant", "m", "request")

	if len(got) != 1 || got[0].CredentialID != 1 {
		t.Fatalf("shadow changed production order: %+v", got)
	}
	waitForShadowCount(t, shadow.OutcomeIdentical, before+1)
}

func TestRouterShadowDiffClassifiesAvailabilityMismatch(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = ursmv2api.ModeShadow
	cfg.ShadowSampleRate = 1
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("set ready: %v", err)
	}

	router := NewRouter(nil, nil)
	t.Cleanup(router.StopShadowWorker)
	router.URSMv2 = mgr
	candidates := []provider.Candidate{{ProviderID: 10, CredentialID: 1, RawModel: "m", Tier: 1, Routable: true}}
	before := shadow.Snapshot()[shadow.OutcomeAvailabilityMismatch]
	got := router.PlanCandidatesWithContext(context.Background(), candidates, nil, &provider.Policy{}, nil, "tenant", "m", "request")

	if len(got) != 1 || got[0].CredentialID != 1 {
		t.Fatalf("shadow changed production order: %+v", got)
	}
	waitForShadowCount(t, shadow.OutcomeAvailabilityMismatch, before+1)
}

func TestRouterShadowDiffSeesCandidateRejectedByLegacy(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = ursmv2api.ModeShadow
	cfg.ShadowSampleRate = 1
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	seed := ursmv2.CandidateSeed{ProviderID: 10, CredentialID: 1, RawModel: "m", TenantID: "tenant"}
	if err := mgr.SetSeedForTest(context.Background(), seed); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	router := NewRouter(nil, nil)
	t.Cleanup(router.StopShadowWorker)
	router.URSMv2 = mgr
	candidates := []provider.Candidate{{
		ProviderID: 10, CredentialID: 1, RawModel: "m", Tier: 1,
		Routable: false,
	}}
	before := shadow.Snapshot()[shadow.OutcomeAvailabilityMismatch]
	got := router.PlanCandidatesWithContext(context.Background(), candidates, nil, &provider.Policy{}, nil, "tenant", "m", "legacy-reject")
	if len(got) != 0 {
		t.Fatalf("legacy production path returned candidates: %+v", got)
	}
	waitForShadowCount(t, shadow.OutcomeAvailabilityMismatch, before+1)
}

func TestRouterShadowDiffRecordsSampledOutAndNotReady(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = ursmv2api.ModeShadow
	cfg.ShadowSampleRate = 0
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)
	router := NewRouter(nil, nil)
	t.Cleanup(router.StopShadowWorker)
	router.URSMv2 = mgr
	candidates := []provider.Candidate{{ProviderID: 10, CredentialID: 1, RawModel: "m", Tier: 1, Routable: true}}

	beforeSampled := shadow.Snapshot()[shadow.OutcomeSampledOut]
	router.PlanCandidatesWithContext(context.Background(), candidates, nil, &provider.Policy{}, nil, "tenant", "m", "sampled")
	waitForShadowCount(t, shadow.OutcomeSampledOut, beforeSampled+1)

	cfg.ShadowSampleRate = 1
	notReadyMgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(notReadyMgr.Close)
	router.URSMv2 = notReadyMgr
	beforeNotReady := shadow.Snapshot()[shadow.OutcomeNotReady]
	router.PlanCandidatesWithContext(context.Background(), candidates, nil, &provider.Policy{}, nil, "tenant", "m", "not-ready")
	waitForShadowCount(t, shadow.OutcomeNotReady, beforeNotReady+1)
}
