package nodestatecache

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// fakeClock 可注入时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Add(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// newTestCache 构造测试缓存：默认小容量、无 ticker、fake clock。
func newTestCache(capacity int32, clk *fakeClock) *Cache {
	if clk == nil {
		clk = newFakeClock(time.Unix(1_700_000_000, 0))
	}
	return New(Options{
		Capacity:     capacity,
		StartTickers: false,
		Clock:        clk.Now,
	})
}

// mustRegister 注册并断言成功。
func mustRegister(c *Cache, i int) int32 {
	id, err := c.Register(NodeRef{
		TenantID:     "tenant-a",
		CredentialID: int64(1000 + i),
		RawModel:     "gpt-test",
	})
	if err != nil {
		panic(fmt.Sprintf("register %d: %v", i, err))
	}
	return id
}

// feedSuccess 喂入成功结果使节点可用。
func feedSuccess(c *Cache, id int32) { c.Update(id, true, ErrKindNone, 50) }

// fakeScorer 幸存集评分桩：记录调用、按配置返回排序结果。
type fakeScorer struct {
	mu       sync.Mutex
	calls    int
	lastSurv []int32
	lastMdl  string
	// order: nodeID -> score；未配置的幸存节点透传（分数 0）。
	order    map[int32]float64
	emptyFor map[string]bool // 对这些模型返回空（模拟无幸存）
}

func (s *fakeScorer) Score(_ context.Context, model, _ string, survivors []int32) []ScoredNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastSurv = append([]int32(nil), survivors...)
	s.lastMdl = model
	if s.emptyFor[model] {
		return nil
	}
	out := make([]ScoredNode, 0, len(survivors))
	for _, id := range survivors {
		out = append(out, ScoredNode{NodeID: id, Score: s.order[id]})
	}
	// 按分数降序（稳定）。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Score > out[j-1].Score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (s *fakeScorer) snapshot() (int, []int32, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]int32(nil), s.lastSurv...), s.lastMdl
}

// fakeSticky sticky 绑定桩。
type fakeSticky struct {
	ref   NodeRef
	valid bool
}

func (s *fakeSticky) StickyNode(_ context.Context, _, _ string) (NodeRef, bool) {
	return s.ref, s.valid
}

// fakeFallback 换模型桩。
type fakeFallback struct {
	models map[string][]string
}

func (f *fakeFallback) FallbackModels(taskType, current string) []string {
	return f.models[current]
}

// fakeRecentSuccess 36h 命中桩：仅 hit 集合内的 ref 返回 true。
type fakeRecentSuccess struct {
	hit map[NodeRef]bool
}

func (f *fakeRecentSuccess) HadSuccessWithin(ref NodeRef, _ time.Duration, _ time.Time) bool {
	return f.hit[ref]
}

// fakeAuthority 权威快照桩。
type fakeAuthority struct {
	records []AuthorityRecord
}

func (f *fakeAuthority) Snapshot(_ context.Context) []AuthorityRecord {
	return f.records
}

// fakeInvalidation 失效监听桩。
type fakeInvalidation struct {
	mu   sync.Mutex
	seen []InvalidationEvent
}

type InvalidationEvent struct {
	Ref    NodeRef
	NodeID int32
	Reason InvalidationReason
}

func (f *fakeInvalidation) OnInvalidate(ref NodeRef, id int32, reason InvalidationReason) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, InvalidationEvent{Ref: ref, NodeID: id, Reason: reason})
}

func (f *fakeInvalidation) events() []InvalidationEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]InvalidationEvent(nil), f.seen...)
}
