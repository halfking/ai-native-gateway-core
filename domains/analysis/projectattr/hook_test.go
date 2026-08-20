package projectattr

import (
	"context"
	"errors"
	"testing"
)

type fakeStore struct {
	authoritative bool
	existing      bool
	signals       Signals
	signalsErr    error
	existingErr   error
	saveErr       error

	saved    []Result
	loadHits int
}

func (f *fakeStore) HasAuthoritativeProject(context.Context, string, string) (bool, error) {
	return f.authoritative, nil
}

func (f *fakeStore) HasAttribution(context.Context, string, string) (bool, error) {
	return f.existing, f.existingErr
}

func (f *fakeStore) LoadSignals(context.Context, string, string) (Signals, error) {
	f.loadHits++
	return f.signals, f.signalsErr
}

func (f *fakeStore) SaveAttribution(_ context.Context, _, _ string, r Result) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, r)
	return nil
}

func (f *fakeStore) LoadProjects(context.Context, string) ([]Project, error) {
	return testProjects(), nil
}

func newHook(t *testing.T, st *fakeStore, opts ...Option) *CloseHook {
	t.Helper()
	return NewCloseHook(st, New(testProjects(), opts...), nil)
}

// 最重要的一条：ACC 权威值存在时，推断整条链路必须让路。
func TestCloseHook_SkipsWhenAuthoritativeProjectExists(t *testing.T) {
	st := &fakeStore{authoritative: true, signals: Signals{UserText: "pms"}}
	h := newHook(t, st)

	if err := h.OnSessionClosed(context.Background(), "t1", "gw_1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.saved) != 0 {
		t.Fatalf("inference must not run when ACC supplied a project; saved %d", len(st.saved))
	}
	if st.loadHits != 0 {
		t.Fatalf("signals must not be loaded when short-circuited; hits=%d", st.loadHits)
	}
	if h.Stats()["skipped_authoritative"] != 1 {
		t.Fatalf("stats = %v", h.Stats())
	}
}

func TestCloseHook_SkipsWhenAlreadyAttributed(t *testing.T) {
	st := &fakeStore{existing: true, signals: Signals{UserText: "pms"}}
	h := newHook(t, st)

	_ = h.OnSessionClosed(context.Background(), "t1", "gw_1")
	if len(st.saved) != 0 {
		t.Fatalf("must not re-attribute an existing session")
	}
	if h.Stats()["skipped_existing"] != 1 {
		t.Fatalf("stats = %v", h.Stats())
	}
}

func TestCloseHook_SavesRuleAttribution(t *testing.T) {
	st := &fakeStore{signals: Signals{UserText: "working on pms today"}}
	h := newHook(t, st)

	if err := h.OnSessionClosed(context.Background(), "t1", "gw_1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.saved) != 1 {
		t.Fatalf("saved %d results, want 1", len(st.saved))
	}
	if st.saved[0].ProjectRef != "p-pms" || st.saved[0].Method != MethodRule {
		t.Fatalf("saved = %+v", st.saved[0])
	}
	if h.Stats()["attributed"] != 1 {
		t.Fatalf("stats = %v", h.Stats())
	}
}

// 判不出来时不写库，避免用空记录污染人工复核队列。
func TestCloseHook_UnresolvedWritesNothing(t *testing.T) {
	st := &fakeStore{signals: Signals{UserText: "nothing matches"}}
	h := newHook(t, st)

	_ = h.OnSessionClosed(context.Background(), "t1", "gw_1")
	if len(st.saved) != 0 {
		t.Fatalf("unresolved sessions must not be persisted; saved %d", len(st.saved))
	}
	if h.Stats()["unresolved"] != 1 {
		t.Fatalf("stats = %v", h.Stats())
	}
}

// 会话没有请求行是正常未命中，不能计入 failed——否则运维会被这类噪声
// 淹没，真正的 DB 故障反而看不见。
func TestCloseHook_NoSignalsCountsAsUnresolvedNotFailed(t *testing.T) {
	st := &fakeStore{signalsErr: ErrNoSignals}
	h := newHook(t, st)

	if err := h.OnSessionClosed(context.Background(), "t1", "gw_1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stats := h.Stats()
	if stats["unresolved"] != 1 {
		t.Fatalf("unresolved = %d, want 1; stats=%v", stats["unresolved"], stats)
	}
	if stats["failed"] != 0 {
		t.Fatalf("failed = %d, want 0 — missing signals is not a failure", stats["failed"])
	}
	if len(st.saved) != 0 {
		t.Fatalf("nothing should be persisted")
	}
}

// 归集是辅助功能，任何失败都不得冒泡去触发事件重试。
func TestCloseHook_ErrorsNeverPropagate(t *testing.T) {
	cases := map[string]*fakeStore{
		"existing check": {existingErr: errors.New("db down")},
		"load signals":   {signalsErr: errors.New("db down")},
		"save":           {signals: Signals{UserText: "pms"}, saveErr: errors.New("db down")},
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHook(t, st)
			if err := h.OnSessionClosed(context.Background(), "t1", "gw_1"); err != nil {
				t.Fatalf("error must not propagate, got %v", err)
			}
			if h.Stats()["failed"] != 1 {
				t.Fatalf("stats = %v", h.Stats())
			}
		})
	}
}

func TestCloseHook_DisabledWhenDependenciesMissing(t *testing.T) {
	h := NewCloseHook(nil, nil, nil)
	if err := h.OnSessionClosed(context.Background(), "t1", "gw_1"); err != nil {
		t.Fatalf("disabled hook must be a no-op, got %v", err)
	}
}

func TestCloseHook_IgnoresEmptyIdentifiers(t *testing.T) {
	st := &fakeStore{signals: Signals{UserText: "pms"}}
	h := newHook(t, st)

	_ = h.OnSessionClosed(context.Background(), "", "gw_1")
	_ = h.OnSessionClosed(context.Background(), "t1", "")
	if len(st.saved) != 0 {
		t.Fatalf("must skip when tenant or session id is empty")
	}
}

// TestCloseHook_AttributorForFactory_OnSessionClosed 验证：注入
// AttributorFor 工厂后，每次 hook 触发都按 (tenant, session) 现取
// Attributor。这是 Stage 5 main_pipeline.go 装配路径；不注入时回落到
// 静态 attributor（向后兼容）。
func TestCloseHook_AttributorForFactory_OnSessionClosed(t *testing.T) {
	st := &fakeStore{signals: Signals{UserText: "pms"}}
	h := NewCloseHook(st, nil, nil)
	var calls int
	h.AttributorFor = func(_ context.Context, tenantID, _ string) (*Attributor, error) {
		calls++
		return New(testProjects(), WithInherit(nil)), nil
	}

	if err := h.OnSessionClosed(context.Background(), "t1", "gw_1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("AttributorFor called %d times, want 1", calls)
	}
	if len(st.saved) != 1 {
		t.Errorf("saved = %d, want 1", len(st.saved))
	}
}

func TestCloseHook_AttributorForReturnsNilIsUnresolved(t *testing.T) {
	st := &fakeStore{signals: Signals{UserText: "pms"}}
	h := NewCloseHook(st, nil, nil)
	h.AttributorFor = func(_ context.Context, _, _ string) (*Attributor, error) {
		return nil, nil // resolver 显式禁用
	}
	_ = h.OnSessionClosed(context.Background(), "t1", "gw_1")
	if len(st.saved) != 0 {
		t.Errorf("nil attributor must not persist: saved=%d", len(st.saved))
	}
	if h.Stats()["unresolved"] != 1 {
		t.Errorf("stats = %v", h.Stats())
	}
}

func TestCloseHook_AttributorForErrorIsFailed(t *testing.T) {
	st := &fakeStore{signals: Signals{UserText: "pms"}}
	h := NewCloseHook(st, nil, nil)
	h.AttributorFor = func(_ context.Context, _, _ string) (*Attributor, error) {
		return nil, errors.New("resolver db down")
	}
	_ = h.OnSessionClosed(context.Background(), "t1", "gw_1")
	if len(st.saved) != 0 {
		t.Errorf("failed factory must not persist: saved=%d", len(st.saved))
	}
	if h.Stats()["failed"] != 1 {
		t.Errorf("failed = %d, want 1; stats=%v", h.Stats()["failed"], h.Stats())
	}
}

// TestCloseHook_DefaultOffDoesNotQueryProjectDim 验证：默认关闭场景下
// （attributor=nil 且 AttributorFor=nil）hook 不会触发任何 DB 操作。
// 这是 Stage 5 "默认 false 不提交启用" 的核心保障：任何配置错误都不会
// 让 hook 偷偷运行。
func TestCloseHook_DefaultOffDoesNotQueryProjectDim(t *testing.T) {
	st := &fakeStore{}
	h := NewCloseHook(st, nil, nil) // 没注入 attributor，没注入 AttributorFor

	if err := h.OnSessionClosed(context.Background(), "t1", "gw_1"); err != nil {
		t.Fatalf("default-off hook must not error, got %v", err)
	}
	// 所有 DB 调用都不应发生。
	if st.loadHits != 0 {
		t.Errorf("LoadSignals called %d times, want 0", st.loadHits)
	}
	if len(st.saved) != 0 {
		t.Errorf("Save called %d times, want 0", len(st.saved))
	}
}
