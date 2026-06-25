package routing

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// 辅助函数：构造一个可用的 Candidate
func makeCandidate(credID int, model, billingMode string, tier int) provider.Candidate {
	return provider.Candidate{
		ProviderID:        1,
		CredentialID:      credID,
		RawModel:          model,
		BillingMode:       billingMode,
		Tier:              tier,
		Weight:            100,
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
	}
}

func newTestRouter() *Router {
	return NewRouter(NewStickyCache(), limiter.New())
}

func TestRouter_PlanCandidates_NoStore_NoFilter(t *testing.T) {
	// RouteNodeStore=nil 时不过滤
	r := newTestRouter()
	candidates := []provider.Candidate{
		makeCandidate(1, "minimax-m3", "token", 1),
		makeCandidate(2, "minimax-m3", "token", 1),
	}

	ordered := r.PlanCandidates(context.Background(), candidates, nil, nil, &provider.Policy{}, nil)
	if len(ordered) != 2 {
		t.Fatalf("expected 2, got %d", len(ordered))
	}
}

func TestRouter_PlanCandidates_FilterByRouteNodeHealth(t *testing.T) {
	// 准备 RouteNodeStore 让 cred 1 不可用
	c := getTestRedis(t)
	defer c.Close()

	store := NewRouteNodeStore(c, DefaultRouteNodeConfig())
	ctx := context.Background()

	const credIDDisabled = 88001
	const credIDEnabled = 88002
	const model = "minimax-m3-health"
	_ = store.Delete(ctx, credIDDisabled, model)
	t.Cleanup(func() { _ = store.Delete(ctx, credIDDisabled, model) })

	// 让 credIDDisabled 连续失败 3 次
	for i := 0; i < 3; i++ {
		_, _, _ = store.RecordFailure(ctx, credIDDisabled, model, "r", "rate_limit")
	}

	r := newTestRouter()
	r.RouteNodeStore = store

	candidates := []provider.Candidate{
		makeCandidate(credIDDisabled, model, "token", 1),
		makeCandidate(credIDEnabled, model, "token", 1),
	}

	ordered := r.PlanCandidates(ctx, candidates, nil, nil, &provider.Policy{}, nil)
	if len(ordered) != 1 {
		t.Fatalf("expected 1 candidate after filter, got %d", len(ordered))
	}
	if ordered[0].CredentialID != credIDEnabled {
		t.Fatalf("filtered[0].CredentialID=%d, want %d", ordered[0].CredentialID, credIDEnabled)
	}
}

func TestRouter_PlanCandidates_SessionPreferredBeatsSticky(t *testing.T) {
	// session 偏好 > client sticky
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-session-pref"
	const credSticky = 89001
	const credPreferred = 89002
	t.Cleanup(func() { _ = prefStore.Clear(ctx, sessionID) })

	// 设置 session 偏好
	if err := prefStore.Set(ctx, sessionID, credPreferred, "minimax-m3"); err != nil {
		t.Fatalf("Set err=%v", err)
	}

	// 设置 client sticky（不同的 credential）
	r := newTestRouter()
	r.SessionPrefStore = prefStore
	sticky := credSticky
	r.Sticky.Set("test:sticky:key", sticky, 60_000_000_000) // 60s

	candidates := []provider.Candidate{
		makeCandidate(89003, "minimax-m3", "token", 1),
		makeCandidate(credSticky, "minimax-m3", "token", 1),
		makeCandidate(credPreferred, "minimax-m3", "token", 1),
	}

	stickyPtr := credSticky
	prefPtr := credPreferred
	ordered := r.PlanCandidates(ctx, candidates, &stickyPtr, &prefPtr, &provider.Policy{}, nil)
	if len(ordered) != 3 {
		t.Fatalf("expected 3, got %d", len(ordered))
	}
	if ordered[0].CredentialID != credPreferred {
		t.Fatalf("first candidate should be session preferred, got %d", ordered[0].CredentialID)
	}
}

func TestRouter_PlanCandidates_StickyUsedWhenNoSessionPref(t *testing.T) {
	// 没有 session 偏好时，client sticky 生效
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	r := newTestRouter()
	r.SessionPrefStore = prefStore

	const credSticky = 89101
	r.Sticky.Set("test:sticky", credSticky, 60_000_000_000)

	candidates := []provider.Candidate{
		makeCandidate(89102, "minimax-m3", "token", 1),
		makeCandidate(credSticky, "minimax-m3", "token", 1),
	}

	stickyPtr := credSticky
	// session 偏好为空
	ordered := r.PlanCandidates(ctx, candidates, &stickyPtr, nil, &provider.Policy{}, nil)
	if ordered[0].CredentialID != credSticky {
		t.Fatalf("first should be sticky when no session pref, got %d", ordered[0].CredentialID)
	}
}

func TestRouter_PlanCandidates_SessionPrefMissingCred_NoEffect(t *testing.T) {
	// session 偏好指向的 credential 不在候选中 → noop
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-session-pref-missing"
	const credPref = 89201 // 不在 candidates 中
	t.Cleanup(func() { _ = prefStore.Clear(ctx, sessionID) })

	_ = prefStore.Set(ctx, sessionID, credPref, "minimax-m3")

	r := newTestRouter()
	r.SessionPrefStore = prefStore

	candidates := []provider.Candidate{
		makeCandidate(89202, "minimax-m3", "token", 1),
		makeCandidate(89203, "minimax-m3", "token", 1),
	}

	prefPtr := credPref
	ordered := r.PlanCandidates(ctx, candidates, nil, &prefPtr, &provider.Policy{}, nil)
	// sessionPref 在候选中找不到时，按 P2C 排序，不强制排首位
	if len(ordered) != 2 {
		t.Fatalf("expected 2, got %d", len(ordered))
	}
}

func TestRouter_PlanCandidates_NilStores_BackwardCompatible(t *testing.T) {
	// RouteNodeStore=nil, SessionPrefStore=nil：向后兼容
	r := newTestRouter()

	candidates := []provider.Candidate{
		makeCandidate(1, "minimax-m3", "token", 1),
	}

	ordered := r.PlanCandidates(context.Background(), candidates, nil, nil, &provider.Policy{}, nil)
	if len(ordered) != 1 {
		t.Fatalf("expected 1, got %d", len(ordered))
	}
}

func TestRouter_PlanCandidates_AllFilteredByHealth_ReturnNil(t *testing.T) {
	// 所有候选都被 RouteNodeState 过滤掉 → 返回 nil
	c := getTestRedis(t)
	defer c.Close()

	store := NewRouteNodeStore(c, DefaultRouteNodeConfig())
	ctx := context.Background()

	const cred1 = 89301
	const cred2 = 89302
	const model = "minimax-m3-allfiltered"
	_ = store.Delete(ctx, cred1, model)
	_ = store.Delete(ctx, cred2, model)
	t.Cleanup(func() {
		_ = store.Delete(ctx, cred1, model)
		_ = store.Delete(ctx, cred2, model)
	})

	// 两个 cred 都触发 Disabled
	for _, id := range []int{cred1, cred2} {
		for i := 0; i < 3; i++ {
			_, _, _ = store.RecordFailure(ctx, id, model, "r", "rate_limit")
		}
	}

	r := newTestRouter()
	r.RouteNodeStore = store

	candidates := []provider.Candidate{
		makeCandidate(cred1, model, "token", 1),
		makeCandidate(cred2, model, "token", 1),
	}

	ordered := r.PlanCandidates(ctx, candidates, nil, nil, &provider.Policy{}, nil)
	if ordered != nil {
		t.Fatalf("expected nil when all filtered, got %v", ordered)
	}
}