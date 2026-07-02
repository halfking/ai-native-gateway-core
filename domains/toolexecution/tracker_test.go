package toolexecution

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
)

// ──────────────────────────────────────────────────────────────────
// In-memory store for testing
// ──────────────────────────────────────────────────────────────────

// memoryStore 简单的线程安全内存实现，仅用于测试。
type memoryStore struct {
	mu    sync.Mutex
	execs map[string]*ToolExecution
	stats map[string]*ToolUsageStats // key = tool_name + "|" + date.UTC().Format("2006-01-02")
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		execs: make(map[string]*ToolExecution),
		stats: make(map[string]*ToolUsageStats),
	}
}

func (m *memoryStore) Save(_ context.Context, e *ToolExecution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.execs[e.ExecutionID]; ok {
		return errors.New("duplicate execution_id")
	}
	cp := *e
	m.execs[e.ExecutionID] = &cp
	return nil
}

func (m *memoryStore) Get(_ context.Context, id string) (*ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.execs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *e
	return &cp, nil
}

func (m *memoryStore) Update(_ context.Context, id string, updater func(*ToolExecution) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.execs[id]
	if !ok {
		return ErrNotFound
	}
	if err := updater(e); err != nil {
		return err
	}
	e.ComputeDuration()
	return nil
}

func (m *memoryStore) ListBySession(_ context.Context, sessionID string) ([]*ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*ToolExecution
	for _, e := range m.execs {
		if e.SessionID == sessionID {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func (m *memoryStore) ListByIdentity(_ context.Context, hash string, limit int) ([]*ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	var out []*ToolExecution
	for _, e := range m.execs {
		if e.IdentityHash == hash {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memoryStore) ListByToolName(_ context.Context, name string, start, end time.Time) ([]*ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*ToolExecution
	for _, e := range m.execs {
		if e.ToolName != name {
			continue
		}
		if e.StartedAt.Before(start) || !e.StartedAt.Before(end) {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

func (m *memoryStore) ListByTenant(_ context.Context, tenantID string, start, end time.Time, limit int) ([]*ToolExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	var out []*ToolExecution
	for _, e := range m.execs {
		if e.TenantID != tenantID {
			continue
		}
		if e.StartedAt.Before(start) || !e.StartedAt.Before(end) {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memoryStore) SaveStats(_ context.Context, s *ToolUsageStats) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := s.ToolName + "|" + s.Date.UTC().Format("2006-01-02")
	if existing, ok := m.stats[key]; ok {
		// 模拟 ON CONFLICT DO UPDATE
		cp := *s
		cp.ID = existing.ID
		cp.CreatedAt = existing.CreatedAt
		m.stats[key] = &cp
		return nil
	}
	cp := *s
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	cp.UpdatedAt = time.Now()
	m.stats[key] = &cp
	return nil
}

func (m *memoryStore) GetStats(_ context.Context, name string, date time.Time) (*ToolUsageStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := name + "|" + date.UTC().Format("2006-01-02")
	s, ok := m.stats[key]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (m *memoryStore) ListStats(_ context.Context, name string, start, end time.Time, limit int) ([]*ToolUsageStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	var out []*ToolUsageStats
	for _, s := range m.stats {
		if name != "" && s.ToolName != name {
			continue
		}
		if s.Date.Before(start) || !s.Date.Before(end) {
			continue
		}
		cp := *s
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memoryStore) ListToolNamesWithActivity(_ context.Context, start, end time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]struct{})
	for _, e := range m.execs {
		if e.StartedAt.Before(start) || !e.StartedAt.Before(end) {
			continue
		}
		seen[e.ToolName] = struct{}{}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// quietLogger 返回一个丢弃所有日志输出的 logger，避免测试输出被淹没。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ──────────────────────────────────────────────────────────────────
// ToolExecution 类型测试
// ──────────────────────────────────────────────────────────────────

func TestExecutionStatus_IsTerminal(t *testing.T) {
	cases := []struct {
		status ExecutionStatus
		want   bool
	}{
		{StatusPending, false},
		{StatusSuccess, true},
		{StatusError, true},
		{StatusTimeout, true},
		{"unknown", false},
	}
	for _, c := range cases {
		e := &ToolExecution{Status: c.status}
		if got := e.IsTerminal(); got != c.want {
			t.Errorf("status %q: IsTerminal=%v, want %v", c.status, got, c.want)
		}
	}
}

func TestExecutionStatus_ComputeDuration(t *testing.T) {
	start := time.Now().UTC()
	exec := &ToolExecution{
		StartedAt:   start,
		CompletedAt: start.Add(250 * time.Millisecond),
	}
	exec.ComputeDuration()
	if exec.DurationMs != 250 {
		t.Errorf("DurationMs=%d, want 250", exec.DurationMs)
	}

	// 零时间不应更新
	exec2 := &ToolExecution{}
	exec2.ComputeDuration()
	if exec2.DurationMs != 0 {
		t.Errorf("empty exec: DurationMs=%d, want 0", exec2.DurationMs)
	}
}

// ──────────────────────────────────────────────────────────────────
// Tracker 测试
// ──────────────────────────────────────────────────────────────────

func TestTracker_RecordStart_Success(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())

	exec := &ToolExecution{
		SessionID: "sess-1",
		RequestID: "req-1",
		TenantID:  "tenant-1",
		ToolName:  "search",
		Arguments: json.RawMessage(`{"q":"hello"}`),
	}
	id, err := tracker.RecordStart(context.Background(), exec)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	if id == "" {
		t.Fatal("execution id is empty")
	}
	if id != exec.ExecutionID {
		t.Errorf("exec.ExecutionID=%q, want %q", exec.ExecutionID, id)
	}
	if exec.Status != StatusPending {
		t.Errorf("Status=%q, want pending", exec.Status)
	}
	if exec.StartedAt.IsZero() {
		t.Error("StartedAt is zero")
	}

	// 持久化的字段一致
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ToolName != "search" {
		t.Errorf("ToolName=%q, want search", got.ToolName)
	}
}

func TestTracker_RecordStart_PreservesGivenID(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	exec := &ToolExecution{
		ExecutionID: "fixed-id",
		ToolName:    "search",
	}
	id, err := tracker.RecordStart(context.Background(), exec)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	if id != "fixed-id" {
		t.Errorf("id=%q, want fixed-id", id)
	}
}

func TestTracker_RecordSuccess(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()

	exec := &ToolExecution{ToolName: "search"}
	id, _ := tracker.RecordStart(ctx, exec)
	// 让 elapsed 大于 0
	time.Sleep(5 * time.Millisecond)

	result := json.RawMessage(`{"hits":3}`)
	if err := tracker.RecordSuccess(ctx, id, result); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	got, _ := store.Get(ctx, id)
	if got.Status != StatusSuccess {
		t.Errorf("Status=%q, want success", got.Status)
	}
	if string(got.Result) != string(result) {
		t.Errorf("Result=%s, want %s", got.Result, result)
	}
	if got.CompletedAt.IsZero() {
		t.Error("CompletedAt is zero")
	}
	if got.DurationMs <= 0 {
		t.Errorf("DurationMs=%d, want > 0", got.DurationMs)
	}
}

func TestTracker_RecordError(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()

	exec := &ToolExecution{ToolName: "search"}
	id, _ := tracker.RecordStart(ctx, exec)
	if err := tracker.RecordError(ctx, id, "boom", ErrorTypeNetwork); err != nil {
		t.Fatalf("RecordError: %v", err)
	}
	got, _ := store.Get(ctx, id)
	if got.Status != StatusError {
		t.Errorf("Status=%q, want error", got.Status)
	}
	if got.ErrorMessage != "boom" {
		t.Errorf("ErrorMessage=%q, want boom", got.ErrorMessage)
	}
	if got.ErrorType != ErrorTypeNetwork {
		t.Errorf("ErrorType=%q, want network_error", got.ErrorType)
	}
}

func TestTracker_RecordError_DefaultType(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()
	exec := &ToolExecution{ToolName: "x"}
	id, _ := tracker.RecordStart(ctx, exec)
	if err := tracker.RecordError(ctx, id, "x", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, id)
	if got.ErrorType != ErrorTypeExecutionFail {
		t.Errorf("ErrorType=%q, want %q", got.ErrorType, ErrorTypeExecutionFail)
	}
}

func TestTracker_RecordTimeout(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()
	exec := &ToolExecution{ToolName: "x"}
	id, _ := tracker.RecordStart(ctx, exec)
	if err := tracker.RecordTimeout(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, id)
	if got.Status != StatusTimeout {
		t.Errorf("Status=%q, want timeout", got.Status)
	}
	if got.ErrorType != ErrorTypeTimeout {
		t.Errorf("ErrorType=%q, want timeout", got.ErrorType)
	}
	if got.ErrorMessage == "" {
		t.Error("ErrorMessage is empty")
	}
}

func TestTracker_UpdateOnMissing_ReturnsNotFound(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	err := tracker.RecordSuccess(context.Background(), "missing", nil)
	if !IsNotFound(err) {
		t.Errorf("err=%v, want IsNotFound", err)
	}
}

func TestTracker_UpdateOnTerminal_StillSucceeds(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()
	exec := &ToolExecution{ToolName: "x"}
	id, _ := tracker.RecordStart(ctx, exec)
	if err := tracker.RecordSuccess(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	// 重复覆盖 — 不应阻塞
	if err := tracker.RecordError(ctx, id, "late", ""); err != nil {
		t.Errorf("second update: %v", err)
	}
	got, _ := store.Get(ctx, id)
	if got.Status != StatusError {
		t.Errorf("Status=%q, want error (overwritten)", got.Status)
	}
}

func TestTracker_GetBySession(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()

	tracker.RecordStart(ctx, &ToolExecution{SessionID: "S1", ToolName: "a"})
	tracker.RecordStart(ctx, &ToolExecution{SessionID: "S1", ToolName: "b"})
	tracker.RecordStart(ctx, &ToolExecution{SessionID: "S2", ToolName: "c"})

	got, err := tracker.GetBySession(ctx, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
}

func TestTracker_GetByIdentity(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		tracker.RecordStart(ctx, &ToolExecution{
			SessionID:    "s",
			IdentityHash: "h1",
			ToolName:     "t",
		})
	}
	got, err := tracker.GetByIdentity(ctx, "h1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len=%d, want 3", len(got))
	}
}

func TestTracker_GetByExecutionID(t *testing.T) {
	store := newMemoryStore()
	tracker := NewTracker(store, quietLogger())
	ctx := context.Background()
	id, _ := tracker.RecordStart(ctx, &ToolExecution{ToolName: "x"})
	got, err := tracker.GetByExecutionID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionID != id {
		t.Errorf("id=%q, want %q", got.ExecutionID, id)
	}
	_, err = tracker.GetByExecutionID(ctx, "nope")
	if !IsNotFound(err) {
		t.Errorf("err=%v, want IsNotFound", err)
	}
}

func TestNewTracker_NilStorePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	NewTracker(nil, nil)
}
