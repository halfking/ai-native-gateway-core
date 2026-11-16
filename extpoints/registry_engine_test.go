package extpoints

import (
	"context"
	"testing"
)

// stubPlugin 是一个最小可注册的扩展点（实现 ExtensionPoint + PhaseExecutor）。
type stubPlugin struct {
	md       PluginMetadata
	execs    int
	failWith error
}

func (s *stubPlugin) Metadata() *PluginMetadata { return &s.md }
func (s *stubPlugin) Initialize(ctx context.Context) error {
	return nil
}
func (s *stubPlugin) Shutdown(ctx context.Context) error { return nil }
func (s *stubPlugin) HealthCheck(ctx context.Context) error {
	return nil
}
func (s *stubPlugin) Execute(ctx *ExecutionContext) (*ExecutionResult, error) {
	s.execs++
	if s.failWith != nil {
		return &ExecutionResult{Success: false, Error: s.failWith}, nil
	}
	return &ExecutionResult{Success: true}, nil
}

func newStub(id string, phase Phase, prio Priority) *stubPlugin {
	return &stubPlugin{md: PluginMetadata{PluginID: id, Phase: phase, Priority: prio}}
}

// TestRegistryGetIsPriorityOrdered 锁住「Get 按优先级升序」。
//
// ★ 这条判据来自 demo 的自述契约（plugin-runtime/demo/integration_test.go：
//
//	"Verify priority order (highest priority first)"）。
func TestRegistryGetIsPriorityOrdered(t *testing.T) {
	r := NewExtensionPointRegistry()
	low := newStub("low", PhasePreAuth, PriorityLow)
	high := newStub("high", PhasePreAuth, PriorityHighest)
	normal := newStub("normal", PhasePreAuth, PriorityNormal)

	for _, p := range []ExtensionPoint{normal, low, high} {
		if err := r.Register(PhasePreAuth, p); err != nil {
			t.Fatalf("Register(%s): %v", p.Metadata().PluginID, err)
		}
	}
	got := r.Get(PhasePreAuth)
	if len(got) != 3 {
		t.Fatalf("Get 期望 3 个扩展点，实际 %d", len(got))
	}
	want := []string{"high", "normal", "low"}
	for i, id := range want {
		if got[i].Metadata().PluginID != id {
			t.Errorf("位置 %d 期望 %s，实际 %s（优先级升序被破坏）", i, id, got[i].Metadata().PluginID)
		}
	}
}

// TestRegistryReRegisterReplaces 锁住「同 pluginID 重复注册 ⇒ 替换而不是叠加」。
func TestRegistryReRegisterReplaces(t *testing.T) {
	r := NewExtensionPointRegistry()
	p1 := newStub("dup", PhasePreAuth, PriorityNormal)
	p2 := newStub("dup", PhaseAuth, PriorityNormal)

	if err := r.Register(PhasePreAuth, p1); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register(PhaseAuth, p2); err != nil {
		t.Fatalf("Re-Register: %v", err)
	}
	if n := len(r.Get(PhasePreAuth)); n != 0 {
		t.Errorf("旧 phase 应被摘掉，实际仍有 %d 个", n)
	}
	if n := len(r.Get(PhaseAuth)); n != 1 {
		t.Errorf("新 phase 期望 1 个，实际 %d 个", n)
	}
	if _, err := r.GetByID("dup"); err != nil {
		t.Errorf("GetByID: %v", err)
	}
}

// TestEngineExecutesRegisteredPhases 锁住「引擎真的调用已注册的 PhaseExecutor」。
func TestEngineExecutesRegisteredPhases(t *testing.T) {
	r := NewExtensionPointRegistry()
	pre := newStub("pre", PhasePreAuth, PriorityNormal)
	log := newStub("log", PhaseLog, PriorityNormal)
	for _, p := range []ExtensionPoint{pre, log} {
		if err := r.Register(p.Metadata().Phase, p); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	eng := NewExecutionEngine(r)
	resp, err := eng.Execute(context.Background(), &Request{RequestID: "r-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp == nil {
		t.Fatal("Execute 必须返回非 nil 的 Response")
	}
	if pre.execs != 1 || log.execs != 1 {
		t.Errorf("两个插件都应各执行 1 次，实际 pre=%d log=%d", pre.execs, log.execs)
	}
	st := eng.GetStats()
	if st.TotalExecutions != 1 {
		t.Errorf("TotalExecutions 期望 1，实际 %d", st.TotalExecutions)
	}
	if st.SuccessfulPhases != 2 {
		t.Errorf("SuccessfulPhases 期望 2，实际 %d", st.SuccessfulPhases)
	}
	if st.FailedPhases != 0 {
		t.Errorf("FailedPhases 期望 0，实际 %d", st.FailedPhases)
	}
}

// TestEngineRecordsFailedPhase 锁住「插件失败 ⇒ 该阶段计失败，且错误被回传」。
func TestEngineRecordsFailedPhase(t *testing.T) {
	r := NewExtensionPointRegistry()
	bad := newStub("bad", PhaseAuth, PriorityNormal)
	bad.failWith = context.Canceled
	if err := r.Register(PhaseAuth, bad); err != nil {
		t.Fatalf("Register: %v", err)
	}
	eng := NewExecutionEngine(r)
	resp, err := eng.Execute(context.Background(), &Request{RequestID: "r-2"})
	if err == nil {
		t.Error("插件失败时 Execute 必须返回错误")
	}
	if resp == nil {
		t.Fatal("失败时也必须返回非 nil 的 Response（调用方要拿得到 ErrorPhase）")
	}
	if resp.ErrorPhase != PhaseAuth {
		t.Errorf("ErrorPhase 期望 %q，实际 %q", PhaseAuth, resp.ErrorPhase)
	}
	st := eng.GetStats()
	if st.FailedPhases != 1 {
		t.Errorf("FailedPhases 期望 1，实际 %d", st.FailedPhases)
	}
}
