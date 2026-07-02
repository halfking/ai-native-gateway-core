package clientprofile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// fakeStore 实现 Store 接口，用于 worker / aggregator 测试。
type fakeStore struct {
	mu sync.Mutex

	profiles  map[string]*ClientProfile
	events    []*ClientBehaviorEvent
	saveErr   error
	upsertErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{profiles: make(map[string]*ClientProfile)}
}

func (f *fakeStore) GetProfile(_ context.Context, hash string) (*ClientProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.profiles[hash]; ok {
		copy := *p
		return &copy, nil
	}
	return nil, nil
}

func (f *fakeStore) UpsertProfile(_ context.Context, p *ClientProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	cp := *p
	// 深拷贝切片/映射避免后续 mutate 影响
	cp.PreferredModels = append([]ModelPreference(nil), p.PreferredModels...)
	cp.TaskDistribution = make(map[string]int64, len(p.TaskDistribution))
	for k, v := range p.TaskDistribution {
		cp.TaskDistribution[k] = v
	}
	cp.ActiveHours = append([]int(nil), p.ActiveHours...)
	f.profiles[p.IdentityHash] = &cp
	return nil
}

func (f *fakeStore) ListProfiles(_ context.Context, _ string, _, _ int) ([]*ProfileSummary, error) {
	return nil, nil
}

func (f *fakeStore) CountProfiles(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (f *fakeStore) SaveEvent(_ context.Context, e *ClientBehaviorEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	cp := *e
	f.events = append(f.events, &cp)
	return nil
}

func (f *fakeStore) GetEvents(_ context.Context, hash string, _, _ time.Time) ([]*ClientBehaviorEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*ClientBehaviorEvent, 0)
	for _, e := range f.events {
		if e.IdentityHash == hash {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) snapshot(hash string) *ClientProfile {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[hash]
	if !ok {
		return nil
	}
	cp := *p
	return &cp
}

func (f *fakeStore) eventCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestProfileWorker_NameAndSubscribed(t *testing.T) {
	w := NewProfileWorker(nil, nil)
	if w.Name() != "client_profile_worker" {
		t.Fatalf("Name = %q", w.Name())
	}
	types := w.SubscribedTypes()
	if len(types) == 0 {
		t.Fatal("SubscribedTypes is empty")
	}
	// 至少应包含 request.completed 和 session.closed
	hasCompleted, hasSession := false, false
	for _, ty := range types {
		if ty == analysis.EventRequestCompleted {
			hasCompleted = true
		}
		if ty == analysis.EventSessionClosed {
			hasSession = true
		}
	}
	if !hasCompleted || !hasSession {
		t.Fatalf("missing required subs: %v", types)
	}
}

func TestProfileWorker_Handle_NoAggregator(t *testing.T) {
	// 防御性：aggregator == nil 时 Handle 不应 panic，且返回 nil
	w := NewProfileWorker(nil, nil)
	evt := analysis.AnalysisEvent{Type: analysis.EventRequestCompleted}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle with nil aggregator: %v", err)
	}
}

func TestProfileWorker_Handle_UnsupportedType(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	evt := analysis.AnalysisEvent{
		Type:    "totally.unknown",
		Payload: map[string]any{"identity_hash": "abc"},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle unknown type should not error: %v", err)
	}
	if store.eventCount() != 0 {
		t.Fatal("unsupported type should not save events")
	}
}

func TestProfileWorker_Handle_NonMapPayload(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	evt := analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: "not-a-map",
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.eventCount() != 0 {
		t.Fatal("non-map payload should be skipped")
	}
}

func TestProfileWorker_Handle_MissingIdentityHash(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	evt := analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"model": "gpt-4"},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if store.eventCount() != 0 {
		t.Fatal("event without identity_hash should be skipped")
	}
}

// longHash 返回 64 字符的模拟 SHA-256 哈希（aggregator 需要 16+ 字符构造 vc-id）
func longHash(suffix string) string {
	const base = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	out := base + suffix
	if len(out) > 64 {
		return out[:64]
	}
	// 不够 64 字符时左侧补 0
	for len(out) < 64 {
		out = "0" + out
	}
	return out
}

func TestProfileWorker_Handle_RequestCompleted_Sync(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	evt := analysis.AnalysisEvent{
		EventID:    "ev-1",
		TenantID:   "t1",
		SessionID:  "s1",
		RequestID:  "r1",
		Type:       analysis.EventRequestCompleted,
		OccurredAt: time.Now(),
		Payload: map[string]any{
			"identity_hash": longHash("1"),
			"model":         "gpt-4",
			"success":       true,
			"tokens_used":   100.0,
			"latency_ms":    250.0,
			"task_type":     TaskTypeCode,
		},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := store.eventCount(); got != 1 {
		t.Fatalf("event count = %d, want 1", got)
	}
	p := store.snapshot(longHash("1"))
	if p == nil {
		t.Fatal("profile not created")
	}
	if p.TotalRequests != 1 {
		t.Errorf("TotalRequests = %d, want 1", p.TotalRequests)
	}
	if len(p.PreferredModels) != 1 || p.PreferredModels[0].ModelName != "gpt-4" {
		t.Errorf("PreferredModels = %+v", p.PreferredModels)
	}
	if p.TaskDistribution[TaskTypeCode] != 1 {
		t.Errorf("TaskDistribution[%s] = %d", TaskTypeCode, p.TaskDistribution[TaskTypeCode])
	}
}

func TestProfileWorker_Handle_RequestCompleted_DefaultSuccess(t *testing.T) {
	// 缺 success 字段时默认 true（request.completed 语义）
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	evt := analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("1"), "model": "gpt-4"},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p := store.snapshot(longHash("1"))
	if p == nil || p.PreferredModels[0].SuccessRate != 1.0 {
		t.Fatalf("default success should yield 1.0 success rate, got %+v", p)
	}
}

func TestProfileWorker_Handle_SessionClosed(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	evt := analysis.AnalysisEvent{
		Type:    analysis.EventSessionClosed,
		Payload: map[string]any{"identity_hash": longHash("1")},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p := store.snapshot(longHash("1"))
	if p == nil {
		t.Fatal("profile not created")
	}
	if p.TotalSessions != 1 {
		t.Errorf("TotalSessions = %d, want 1", p.TotalSessions)
	}
}

func TestProfileWorker_Handle_FailureDetected(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	// 先发一个成功的 request 完成
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("1"), "model": "gpt-4", "success": true},
	})
	// 再发一个失败
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:    analysis.EventFailureDetected,
		Payload: map[string]any{"identity_hash": longHash("1")},
	})
	p := store.snapshot(longHash("1"))
	if p == nil {
		t.Fatal("profile not created")
	}
	if p.ErrorRate == 0 {
		t.Errorf("ErrorRate should be > 0 after failure, got %f", p.ErrorRate)
	}
}

func TestProfileWorker_Handle_ApprovalDecided(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("1"), "model": "gpt-4", "success": true},
	})
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		Type:    analysis.EventApprovalDecided,
		Payload: map[string]any{"identity_hash": longHash("1"), "approved": true},
	})
	p := store.snapshot(longHash("1"))
	if p == nil || p.ApprovalRate == 0 {
		t.Fatalf("ApprovalRate should be > 0, got %+v", p)
	}
}

func TestProfileWorker_Handle_Async_StoreErrorDoesNotBlock(t *testing.T) {
	store := newFakeStore()
	store.saveErr = errors.New("db down")
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	// async default = true
	evt := analysis.AnalysisEvent{
		EventID: "ev-async",
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("1")},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle should not error even if store fails async: %v", err)
	}
	// 等异步 goroutine 跑完
	time.Sleep(100 * time.Millisecond)
}

func TestProfileWorker_FlushAndReset(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false) // 让 Handle 同步走，不走 goroutine
	w.SetUpdateTimeout(2 * time.Second)

	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		EventID: "ev-1",
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("1"), "model": "gpt-4"},
	})
	_ = w.Handle(context.Background(), analysis.AnalysisEvent{
		EventID: "ev-2",
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("1"), "model": "gpt-4"},
	})
	if w.PendingCount() != 2 {
		t.Fatalf("PendingCount = %d, want 2", w.PendingCount())
	}
	if err := w.FlushAndReset(context.Background()); err != nil {
		t.Fatalf("FlushAndReset: %v", err)
	}
	if w.PendingCount() != 0 {
		t.Fatalf("after flush PendingCount = %d, want 0", w.PendingCount())
	}
}

func TestProfileWorker_ConvertFieldCoercion(t *testing.T) {
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	// 数值字段以 int/int32/int64/float64 形式都应被正确处理
	evt := analysis.AnalysisEvent{
		Type: analysis.EventRequestCompleted,
		Payload: map[string]any{
			"identity_hash": longHash("2"),
			"tokens_used":   int32(50),
			"latency_ms":    int64(1234),
			"success":       true,
		},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p := store.snapshot(longHash("2"))
	if p == nil {
		t.Fatal("profile not created")
	}
	if p.AvgTokensPerTurn != 50 {
		t.Errorf("AvgTokensPerTurn = %f, want 50", p.AvgTokensPerTurn)
	}
}

func TestProfileWorker_Handle_PayloadFallback(t *testing.T) {
	// session_id/request_id 来自 payload 时应能正确填充
	store := newFakeStore()
	agg := NewAggregator(store, nil)
	w := NewProfileWorker(agg, nil)
	w.SetAsync(false)

	evt := analysis.AnalysisEvent{
		Type:    analysis.EventRequestCompleted,
		Payload: map[string]any{"identity_hash": longHash("2"), "session_id": "s-from-payload"},
	}
	if err := w.Handle(context.Background(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(store.events) == 0 {
		t.Fatal("no event saved")
	}
	if store.events[0].SessionID != "s-from-payload" {
		t.Errorf("SessionID = %q, want s-from-payload", store.events[0].SessionID)
	}
}
