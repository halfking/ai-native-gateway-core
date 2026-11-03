// wiring_test.go — W3 挂载点行为：门关透传 / 门内预热+注入 / settings 同步。
package memoraprewarm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeReader：线程安全（Trigger goroutine 与 Apply 同进程并发）。
type fakeReader struct {
	mu   sync.Mutex
	vals map[string]string
}

func newFakeReader() *fakeReader { return &fakeReader{vals: map[string]string{}} }

func (f *fakeReader) ReadMemoraContext(_ context.Context, sessionID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vals[sessionID], nil
}

func (f *fakeReader) set(sessionID, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vals[sessionID] = raw
}

// 1) 门关 ⇒ 纯透传：不预热、不注入、字节不变。
func TestApplyGateClosedPassthrough(t *testing.T) {
	c := &fakeClient{ok: true}
	w := &fakeWriter{}
	r := newFakeReader()
	r.set("s1", `{"items":[{"content":"记忆"}]}`)
	wr := NewWiring(c, w, r, NewGate(), nil) // 未 Set ⇒ 关
	body := []byte(`{"messages":[{"role":"user","content":"q"}]}`)
	out, injected := wr.Apply(context.Background(), "t", "s1", body)
	if injected || string(out) != string(body) {
		t.Fatal("closed gate must be byte-identical passthrough")
	}
	time.Sleep(50 * time.Millisecond)
	if c.calls.Load() != 0 {
		t.Fatal("closed gate must not prewarm")
	}
}

// 2) 门开 + 旁路有值 ⇒ 触发预热 + 注入落到最后一条 user。
func TestApplyGateOpenWarmsAndInjects(t *testing.T) {
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &fakeWriter{}
	r := newFakeReader()
	r.set("s1", `{"items":[{"content":"用户偏好深色主题"}]}`)
	g := NewGate()
	g.Set(true, nil)
	wr := NewWiring(c, w, r, g, nil)
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"配色"}]}`)
	out, injected := wr.Apply(context.Background(), "acme", "s1", body)
	if !injected {
		t.Fatal("gate-on + hit must inject")
	}
	if !strings.Contains(string(out), "配色\\n\\n【长期记忆参考") {
		t.Fatalf("injection must append to last user message: %s", out)
	}
	if strings.Count(string(out), "【长期记忆参考") != 1 {
		t.Fatal("exactly one injection")
	}
	waitFor(t, func() bool { return c.calls.Load() == 1 && w.calls.Load() == 1 })
}

// 3) 门开 + 旁路空 ⇒ 预热照发、零注入（首请求形态）。
func TestApplyGateOpenMissNoInjection(t *testing.T) {
	c := &fakeClient{ok: true}
	w := &fakeWriter{}
	g := NewGate()
	g.Set(true, nil)
	wr := NewWiring(c, w, newFakeReader(), g, nil)
	body := []byte(`{"messages":[{"role":"user","content":"首问"}]}`)
	out, injected := wr.Apply(context.Background(), "t", "s-miss", body)
	if injected || string(out) != string(body) {
		t.Fatal("miss must not change body")
	}
	waitFor(t, func() bool { return c.calls.Load() == 1 })
}

// 4) nil 组件安全性：全 nil wiring 不 panic。
func TestApplyNilSafety(t *testing.T) {
	var wr *Wiring
	body := []byte(`{"messages":[{"role":"user","content":"q"}]}`)
	if out, injected := wr.Apply(context.Background(), "t", "s", body); injected || string(out) != string(body) {
		t.Fatal("nil wiring must passthrough")
	}
	empty := NewWiring(nil, nil, nil, nil, nil)
	g := NewGate()
	g.Set(true, nil)
	wired := NewWiring(&fakeClient{ok: true}, nil, newFakeReader(), g, nil)
	_ = empty
	if _, injected := wired.Apply(context.Background(), "t", "s", body); injected {
		t.Fatal("nil reader must not inject")
	}
}

// 5) parseAllowlist：空白/逗号混合。
func TestParseAllowlist(t *testing.T) {
	if got := parseAllowlist(""); got != nil {
		t.Fatalf("empty: %v", got)
	}
	if got := parseAllowlist(" , "); got != nil {
		t.Fatalf("blank only: %v", got)
	}
	got := parseAllowlist(" acme , beta,, ")
	if len(got) != 2 || got[0] != "acme" || got[1] != "beta" {
		t.Fatalf("got %v", got)
	}
}

// 6) SyncGateFromSettings：Global 未初始化 ⇒ fallback 关（fail-closed）。
func TestSyncGateFromSettingsNilGlobal(t *testing.T) {
	g := NewGate()
	g.Set(true, nil) // 先开
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	SyncGateFromSettings(ctx, g, 20*time.Millisecond) // Global=nil ⇒ Set(false, nil)
	if g.Allowed("t") {
		t.Fatal("nil settings Global must sync gate closed")
	}
}
