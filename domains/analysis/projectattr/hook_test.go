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
