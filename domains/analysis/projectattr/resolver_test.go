package projectattr

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLoader struct {
	mu       sync.Mutex
	projects map[string][]Project
	err      error
	calls    atomic.Int32
}

func (f *fakeLoader) LoadProjects(_ context.Context, tenantID string) ([]Project, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.projects[tenantID], nil
}

func TestNewProjectResolver_NilStore(t *testing.T) {
	if r := NewProjectResolver(nil); r != nil {
		t.Fatalf("NewProjectResolver(nil) = %v, want nil", r)
	}
}

func TestProjects_NilResolverReturnsEmpty(t *testing.T) {
	var r *ProjectResolver
	got, err := r.Projects(context.Background(), "t")
	if got != nil || err != nil {
		t.Errorf("nil resolver: got (%v, %v)", got, err)
	}
}

func TestProjects_TenantIsolation(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{
		"t1": {{Ref: "p1", Name: "P1"}},
		"t2": {{Ref: "p2", Name: "P2"}},
	}}
	r := NewProjectResolver(store)

	g1, _ := r.Projects(context.Background(), "t1")
	g2, _ := r.Projects(context.Background(), "t2")
	if len(g1) != 1 || g1[0].Ref != "p1" {
		t.Errorf("t1: %+v", g1)
	}
	if len(g2) != 1 || g2[0].Ref != "p2" {
		t.Errorf("t2: %+v", g2)
	}
}

func TestProjects_TTLCaching(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{
		"t1": {{Ref: "p1"}},
	}}
	now := time.Now()
	current := now
	r := NewProjectResolver(store, WithResolverTTL(time.Second), WithResolverClock(func() time.Time { return current }))

	_, _ = r.Projects(context.Background(), "t1")
	_, _ = r.Projects(context.Background(), "t1")
	if got := store.calls.Load(); got != 1 {
		t.Errorf("calls after 2 reads in TTL = %d, want 1", got)
	}

	current = now.Add(2 * time.Second)
	_, _ = r.Projects(context.Background(), "t1")
	if got := store.calls.Load(); got != 2 {
		t.Errorf("calls after TTL expiry = %d, want 2", got)
	}
}

func TestProjects_TTLZeroNeverExpires(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{
		"t1": {{Ref: "p1"}},
	}}
	now := time.Now()
	current := now
	r := NewProjectResolver(store, WithResolverTTL(0), WithResolverClock(func() time.Time { return current }))

	current = now.Add(time.Hour)
	_, _ = r.Projects(context.Background(), "t1")
	_, _ = r.Projects(context.Background(), "t1")
	if got := store.calls.Load(); got != 1 {
		t.Errorf("TTL=0 should never expire; calls = %d", got)
	}
}

func TestProjects_Invalidate(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{
		"t1": {{Ref: "p1"}},
	}}
	r := NewProjectResolver(store)
	_, _ = r.Projects(context.Background(), "t1")
	_, _ = r.Projects(context.Background(), "t1")
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("warmup calls = %d, want 1", got)
	}

	r.Invalidate("t1")
	_, _ = r.Projects(context.Background(), "t1")
	if got := store.calls.Load(); got != 2 {
		t.Errorf("after invalidate calls = %d, want 2", got)
	}
	stats := r.Stats()
	if stats.Invalidations != 1 {
		t.Errorf("invalidations = %d, want 1", stats.Invalidations)
	}
}

func TestProjects_InvalidateIsIdempotent(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{}}
	r := NewProjectResolver(store)
	r.Invalidate("ghost-tenant") // 没缓存条目也要安全
	r.Invalidate("ghost-tenant")
	stats := r.Stats()
	if stats.Invalidations != 2 {
		t.Errorf("invalidations = %d, want 2", stats.Invalidations)
	}
}

func TestProjects_InvalidateHookFires(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{}}
	var fired atomic.Int32
	r := NewProjectResolver(store, WithInvalidationHook(func(string) { fired.Add(1) }))
	r.Invalidate("t")
	if got := fired.Load(); got != 1 {
		t.Errorf("hook fired = %d, want 1", got)
	}
}

func TestProjects_DBErrorIsPropagatedAndCached(t *testing.T) {
	// DB 错一次后 resolver 应该返回 (nil, err)，但同时把这条负缓存
	// 写下来——否则下一次 miss 会再把 DB 打爆一次。
	dbErr := errors.New("db down")
	store := &fakeLoader{err: dbErr}
	now := time.Now()
	current := now
	r := NewProjectResolver(store, WithResolverTTL(time.Second), WithResolverClock(func() time.Time { return current }))

	_, err := r.Projects(context.Background(), "t1")
	if !errors.Is(err, dbErr) {
		t.Fatalf("first err = %v, want %v", err, dbErr)
	}
	// TTL 内：第二次不会回到 store
	_, err2 := r.Projects(context.Background(), "t1")
	if !errors.Is(err2, dbErr) {
		t.Errorf("second err = %v, want %v", err2, dbErr)
	}
	if got := store.calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (negative cache should suppress)", got)
	}
}

func TestProjects_EmptyResultIsValid(t *testing.T) {
	// store 返回空切片（合法："tenant 没有项目"），不应当被当作错误。
	store := &fakeLoader{projects: map[string][]Project{}}
	r := NewProjectResolver(store)
	got, err := r.Projects(context.Background(), "t1")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != nil && len(got) != 0 {
		t.Errorf("expected empty result, got %+v", got)
	}
}

func TestBuildAttributor_NoLLMOption(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{
		"t1": {{Ref: "p1", Name: "P1", MatchKeywords: []string{"kitchen"}}},
	}}
	r := NewProjectResolver(store)
	attr, err := r.BuildAttributor(context.Background(), "t1")
	if err != nil {
		t.Fatalf("BuildAttributor: %v", err)
	}
	if attr == nil {
		t.Fatal("nil attributor")
	}
	if attr.llm != nil {
		t.Error("Attributor must NOT have LLM injected (plan: default off)")
	}
	if attr.inherit == nil {
		t.Error("Attributor should have inherit lookup")
	}

	res := attr.Attribute(context.Background(), Signals{UserText: "kitchen today"})
	if res.ProjectRef != "p1" {
		t.Errorf("rule tier should match: %+v", res)
	}
}

func TestBuildAttributor_NilResolverIsSafe(t *testing.T) {
	var r *ProjectResolver
	attr, err := r.BuildAttributor(context.Background(), "t1")
	if attr != nil || err != nil {
		t.Errorf("nil resolver: got (%v, %v)", attr, err)
	}
}

func TestBuildAttributor_EmptyStoreReturnsAttributorWithNoProjects(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{}}
	r := NewProjectResolver(store)
	attr, err := r.BuildAttributor(context.Background(), "t1")
	if err != nil || attr == nil {
		t.Fatalf("BuildAttributor: %v, %v", attr, err)
	}
	if len(attr.projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(attr.projects))
	}
	// 无规则匹配时，结果应留空（继承层和 LLM 层也未命中）。
	res := attr.Attribute(context.Background(), Signals{UserText: "anything"})
	if res.Found() {
		t.Errorf("empty catalogue should not produce any attribution: %+v", res)
	}
}

// TestProjects_ConcurrentMissCollides 验证 singleflight：N 个并发 miss
// 只触发一次 DB 调用。如果不合并，DB 会被打爆。
func TestProjects_ConcurrentMissCollides(t *testing.T) {
	store := &concLoader{}
	r := NewProjectResolver(store)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Projects(context.Background(), "t1")
		}()
	}
	wg.Wait()
	// 期望最多 1 次 loadFromStore 调用；现实中 singleflight 在并发 miss
	// 时合并为 1 次；50 个 goroutine 全部从同一次加载中读取。
	if got := store.calls.Load(); got != 1 {
		t.Errorf("concurrent miss calls = %d, want 1 (singleflight failure)", got)
	}
}

// TestProjects_ConcurrentMissAcrossTenantsDontCollide 验证不同 tenant 的
// miss 不会被合并为一次 DB 调用（租户隔离）。
func TestProjects_ConcurrentMissAcrossTenantsDontCollide(t *testing.T) {
	store := &concLoader{}
	r := NewProjectResolver(store)

	var wg sync.WaitGroup
	for _, tenant := range []string{"t1", "t2", "t3"} {
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(tn string) {
				defer wg.Done()
				_, _ = r.Projects(context.Background(), tn)
			}(tenant)
		}
	}
	wg.Wait()
	// 期望恰好 3 次 loadFromStore：每个 tenant 一次。
	if got := store.calls.Load(); got != 3 {
		t.Errorf("per-tenant miss calls = %d, want 3", got)
	}
}

// concLoader 在 LoadProjects 内做小延迟，模拟真实 DB 调用，触发 singleflight
// 路径。返回值是 deterministic dummy。
type concLoader struct {
	calls atomic.Int32
}

func (l *concLoader) LoadProjects(_ context.Context, tenantID string) ([]Project, error) {
	l.calls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []Project{{Ref: "p-" + tenantID, Name: "Project " + tenantID}}, nil
}

func TestStats_CountHitsMisses(t *testing.T) {
	store := &fakeLoader{projects: map[string][]Project{"t1": {{Ref: "p1"}}}}
	r := NewProjectResolver(store)
	for i := 0; i < 5; i++ {
		_, _ = r.Projects(context.Background(), "t1")
	}
	stats := r.Stats()
	if stats.CacheHits != 4 {
		t.Errorf("hits = %d, want 4", stats.CacheHits)
	}
	if stats.CacheMisses != 1 {
		t.Errorf("misses = %d, want 1", stats.CacheMisses)
	}
}
