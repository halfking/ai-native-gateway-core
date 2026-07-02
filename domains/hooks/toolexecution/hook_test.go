package toolexecution_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	hookpkg "github.com/kaixuan/llm-gateway-go/domains/hooks/toolexecution"
	toolexecution "github.com/kaixuan/llm-gateway-go/domains/toolexecution"
)

// ──────────────────────────────────────────────────────────────────
// 内存 store — 实现 toolexecution.Store 全接口
// ──────────────────────────────────────────────────────────────────

type memStore struct {
	mu    sync.Mutex
	execs map[string]*toolexecution.ToolExecution
	stats map[string]*toolexecution.ToolUsageStats
}

func newMemStore() *memStore {
	return &memStore{
		execs: map[string]*toolexecution.ToolExecution{},
		stats: map[string]*toolexecution.ToolUsageStats{},
	}
}

func (m *memStore) Save(_ context.Context, e *toolexecution.ToolExecution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.execs[e.ExecutionID]; ok {
		return errors.New("dup")
	}
	cp := *e
	m.execs[e.ExecutionID] = &cp
	return nil
}
func (m *memStore) Get(_ context.Context, id string) (*toolexecution.ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.execs[id]
	if !ok {
		return nil, toolexecution.ErrNotFound
	}
	cp := *e
	return &cp, nil
}
func (m *memStore) Update(_ context.Context, id string, upd func(*toolexecution.ToolExecution) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.execs[id]
	if !ok {
		return toolexecution.ErrNotFound
	}
	if err := upd(e); err != nil {
		return err
	}
	return nil
}
func (m *memStore) ListBySession(_ context.Context, s string) ([]*toolexecution.ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*toolexecution.ToolExecution
	for _, e := range m.execs {
		if e.SessionID == s {
			cp := *e
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (m *memStore) ListByIdentity(_ context.Context, h string, _ int) ([]*toolexecution.ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*toolexecution.ToolExecution
	for _, e := range m.execs {
		if e.IdentityHash == h {
			cp := *e
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (m *memStore) ListByToolName(_ context.Context, n string, start, end time.Time) ([]*toolexecution.ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*toolexecution.ToolExecution
	for _, e := range m.execs {
		if e.ToolName != n {
			continue
		}
		if e.StartedAt.Before(start) || !e.StartedAt.Before(end) {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	return out, nil
}
func (m *memStore) ListByTenant(_ context.Context, tid string, _, _ time.Time, _ int) ([]*toolexecution.ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*toolexecution.ToolExecution
	for _, e := range m.execs {
		if e.TenantID == tid {
			cp := *e
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (m *memStore) SaveStats(_ context.Context, s *toolexecution.ToolUsageStats) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := s.ToolName + "|" + s.Date.UTC().Format("2006-01-02")
	if existing, ok := m.stats[k]; ok {
		cp := *s
		cp.ID = existing.ID
		cp.CreatedAt = existing.CreatedAt
		m.stats[k] = &cp
		return nil
	}
	cp := *s
	cp.UpdatedAt = time.Now()
	m.stats[k] = &cp
	return nil
}
func (m *memStore) GetStats(_ context.Context, n string, d time.Time) (*toolexecution.ToolUsageStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := n + "|" + d.UTC().Format("2006-01-02")
	s, ok := m.stats[k]
	if !ok {
		return nil, toolexecution.ErrNotFound
	}
	cp := *s
	return &cp, nil
}
func (m *memStore) ListStats(_ context.Context, n string, _, _ time.Time, _ int) ([]*toolexecution.ToolUsageStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*toolexecution.ToolUsageStats
	for _, s := range m.stats {
		if n == "" || s.ToolName == n {
			cp := *s
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}
func (m *memStore) ListToolNamesWithActivity(_ context.Context, start, end time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]struct{}{}
	for _, e := range m.execs {
		if e.StartedAt.Before(start) || !e.StartedAt.Before(end) {
			continue
		}
		seen[e.ToolName] = struct{}{}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ──────────────────────────────────────────────────────────────────
// 测试用的 SessionInfo 实现
// ──────────────────────────────────────────────────────────────────

type fakeInfo struct {
	sessionID, requestID, tenantID, model, identity string
}

func (f *fakeInfo) GetSessionID() string    { return f.sessionID }
func (f *fakeInfo) GetRequestID() string    { return f.requestID }
func (f *fakeInfo) GetTenantID() string     { return f.tenantID }
func (f *fakeInfo) GetClientModel() string  { return f.model }
func (f *fakeInfo) GetIdentityHash() string { return f.identity }

// ──────────────────────────────────────────────────────────────────
// hook 测试
// ──────────────────────────────────────────────────────────────────

func TestHook_BeforeAfter_Success(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())

	info := &fakeInfo{
		sessionID: "sess-1",
		requestID: "req-1",
		tenantID:  "t-1",
		model:     "gpt-4",
		identity:  "user-abc",
	}

	id, err := hook.BeforeToolCall(context.Background(), info, "search", "call-1", json.RawMessage(`{"q":"hi"}`))
	if err != nil {
		t.Fatalf("BeforeToolCall: %v", err)
	}
	if id == "" {
		t.Fatal("empty executionID")
	}

	hook.AfterToolCall(context.Background(), id, map[string]any{"hits": 3}, nil)

	got, _ := store.Get(context.Background(), id)
	if got.Status != toolexecution.StatusSuccess {
		t.Errorf("Status=%q, want success", got.Status)
	}
	if got.IdentityHash != "user-abc" {
		t.Errorf("IdentityHash=%q, want user-abc", got.IdentityHash)
	}
	if got.Model != "gpt-4" {
		t.Errorf("Model=%q, want gpt-4", got.Model)
	}
}

func TestHook_After_Error_Network(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())

	info := &fakeInfo{sessionID: "s", requestID: "r", tenantID: "t", identity: "u1"}
	id, _ := hook.BeforeToolCall(context.Background(), info, "fetch", "c-1", nil)
	hook.AfterToolCall(context.Background(), id, nil, errors.New("connection refused: dial tcp"))

	got, _ := store.Get(context.Background(), id)
	if got.Status != toolexecution.StatusError {
		t.Errorf("Status=%q, want error", got.Status)
	}
	if got.ErrorType != toolexecution.ErrorTypeNetwork {
		t.Errorf("ErrorType=%q, want network_error", got.ErrorType)
	}
}

func TestHook_After_Error_Timeout(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())
	info := &fakeInfo{sessionID: "s"}
	id, _ := hook.BeforeToolCall(context.Background(), info, "t", "c", nil)
	hook.AfterToolCall(context.Background(), id, nil, errors.New("i/o timeout"))

	got, _ := store.Get(context.Background(), id)
	if got.Status != toolexecution.StatusError {
		t.Errorf("Status=%q, want error", got.Status)
	}
	if got.ErrorType != toolexecution.ErrorTypeTimeout {
		t.Errorf("ErrorType=%q, want timeout", got.ErrorType)
	}
}

func TestHook_RecordTimeout(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())
	info := &fakeInfo{sessionID: "s"}
	id, _ := hook.BeforeToolCall(context.Background(), info, "t", "c", nil)
	hook.RecordTimeout(context.Background(), id)
	got, _ := store.Get(context.Background(), id)
	if got.Status != toolexecution.StatusTimeout {
		t.Errorf("Status=%q, want timeout", got.Status)
	}
}

func TestHook_After_EmptyID_Ignored(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())
	// 不应 panic
	hook.AfterToolCall(context.Background(), "", nil, nil)
	hook.RecordTimeout(context.Background(), "")
}

func TestHook_NilInfo_Errors(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())
	_, err := hook.BeforeToolCall(context.Background(), nil, "t", "c", nil)
	if err == nil {
		t.Error("expected error on nil info")
	}
}

func TestHook_After_UnserializableResult(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())

	info := &fakeInfo{sessionID: "s"}
	id, _ := hook.BeforeToolCall(context.Background(), info, "t", "c", nil)
	// chan 无法序列化
	hook.AfterToolCall(context.Background(), id, make(chan int), nil)

	got, _ := store.Get(context.Background(), id)
	if got.Status != toolexecution.StatusError {
		t.Errorf("Status=%q, want error (unserializable result)", got.Status)
	}
}

func TestHook_ClassifyError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("deadline exceeded"), toolexecution.ErrorTypeTimeout},
		{errors.New("connection reset"), toolexecution.ErrorTypeNetwork},
		{errors.New("invalid argument: foo"), toolexecution.ErrorTypeInvalidArgs},
		{errors.New("random thing"), toolexecution.ErrorTypeExecutionFail},
	}
	for _, c := range cases {
		got := hookpkg.ClassifyErrorForTest(c.err)
		if got != c.want {
			t.Errorf("err=%q: got %q, want %q", c.err, got, c.want)
		}
	}
}

func TestNewHook_NilTrackerPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	hookpkg.NewHook(nil, nil)
}

func TestHook_PipelineHook_Properties(t *testing.T) {
	store := newMemStore()
	tracker := toolexecution.NewTracker(store, quiet())
	hook := hookpkg.NewHook(tracker, quiet())
	if hook.Name() != "toolexecution.track" {
		t.Errorf("Name=%q, want toolexecution.track", hook.Name())
	}
	if hook.Priority() != 60 {
		t.Errorf("Priority=%d, want 60", hook.Priority())
	}
	if hook.Enabled(context.Background(), nil) {
		t.Error("Enabled(nil env) should be false")
	}
	// Execute / OnError 为 no-op，不应 panic
	_ = hook.Execute(context.Background(), nil)
	_ = hook.OnError(context.Background(), nil, errors.New("x"))
}

func TestSessionInfo_FakeInfo(t *testing.T) {
	f := &fakeInfo{sessionID: "s", requestID: "r", tenantID: "t", model: "m", identity: "i"}
	if f.GetSessionID() != "s" {
		t.Error("sessionID")
	}
	if f.GetRequestID() != "r" {
		t.Error("requestID")
	}
	if f.GetTenantID() != "t" {
		t.Error("tenantID")
	}
	if f.GetClientModel() != "m" {
		t.Error("model")
	}
	if f.GetIdentityHash() != "i" {
		t.Error("identity")
	}
}
