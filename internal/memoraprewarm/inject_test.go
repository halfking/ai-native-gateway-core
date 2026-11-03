// inject_test.go — W2 注入半边单测：有界渲染 / 对照组零影响 / E2E
// （旁路写 → 读 → 注入出现在出站 prompt 尾部）。
package memoraprewarm

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenderInjectionBoundedAndLabeled(t *testing.T) {
	items := make([]map[string]any, 8)
	for i := range items {
		items[i] = map[string]any{"memory_id": string(rune('a' + i)), "content": strings.Repeat("记", 480)}
	}
	raw, _ := json.Marshal(map[string]any{"items": items})
	got := RenderInjection(string(raw))
	if got == "" {
		t.Fatal("non-empty items must render")
	}
	if !strings.HasPrefix(got, "【长期记忆参考") {
		t.Fatalf("missing source label header: %.40s", got)
	}
	if len(got) > MaxInjectionBytes+3 { // 头+换行余量
		t.Fatalf("injection = %d bytes, exceeds %d budget", len(got), MaxInjectionBytes)
	}
	lines := strings.Count(got, "\n- ")
	if lines == 0 || lines > 8 {
		t.Fatalf("rendered lines = %d", lines)
	}
}

func TestRenderInjectionDegenerateInputs(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":        "",
		"blank":        "   ",
		"bad json":     "{not-json",
		"no items":     `{"items":[]}`,
		"empty bodies": `{"items":[{"content":"  "}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := RenderInjection(raw); got != "" {
				t.Fatalf("degenerate input must render empty, got %.40s", got)
			}
		})
	}
}

// 对照组零影响：无注入块 ⇒ prompt 原样（字节不变）。
func TestInjectPromptControlGroupUntouched(t *testing.T) {
	prompt := "你是一个助手。\n用户：生成界面配色"
	if got := InjectPrompt(prompt, ""); got != prompt {
		t.Fatalf("empty injection must return prompt unchanged:\n%q", got)
	}
}

// 注入出现在尾部且带来源标注。
func TestInjectPromptAppendsAtTail(t *testing.T) {
	prompt := "系统指令"
	inj := RenderInjection(`{"items":[{"memory_id":"m1","content":"偏好深色主题"}]}`)
	got := InjectPrompt(prompt, inj)
	if !strings.HasPrefix(got, prompt) {
		t.Fatal("prompt head must be preserved")
	}
	if !strings.HasSuffix(got, inj) {
		t.Fatal("injection must be at the tail")
	}
	if !strings.Contains(got, "偏好深色主题") || !strings.Contains(got, "memora") {
		t.Fatalf("injection content/label missing: %s", got)
	}
}

// E2E：Trigger 旁路写 → ReadAndRender 读 → 注入出站 prompt 尾部；
// 对照 session（未预热）读取为空、prompt 不变。
func TestEndToEndSideWriteThenInject(t *testing.T) {
	mem := newMemFake()
	w := &countingWriter{inner: &memWriter{mem: mem}}
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"memory_id":"m1","content":"用户偏好深色主题"}]}`)}
	Trigger(c, w, nil, nil, "tenant", "sess-e2e", "生成配色", time.Hour)
	waitFor(t, func() bool { return atomic.LoadInt32(&w.calls) == 1 })

	// 读侧：预热过的 session 有注入。
	r := &memReader{mem: mem}
	inj := ReadAndRender(context.Background(), r, "sess-e2e")
	if inj == "" || !strings.Contains(inj, "深色主题") {
		t.Fatalf("prewarmed session injection = %q", inj)
	}
	prompt := "系统指令"
	got := InjectPrompt(prompt, inj)
	if !strings.HasSuffix(got, inj) {
		t.Fatal("E2E: injection must land at prompt tail")
	}

	// 对照组：未预热的 session 读空、prompt 不变。
	ctl := ReadAndRender(context.Background(), r, "sess-control")
	if ctl != "" {
		t.Fatalf("control session must read empty, got %q", ctl)
	}
	if InjectPrompt(prompt, ctl) != prompt {
		t.Fatal("control prompt must be unchanged")
	}
}

// —— 最小内存 fake（旁路读写共享）——

type memFake struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemFake() *memFake { return &memFake{m: map[string]string{}} }

func (f *memFake) set(k, v string) {
	f.mu.Lock()
	f.m[k] = v
	f.mu.Unlock()
}

func (f *memFake) get(k string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[k]
}

type memWriter struct{ mem *memFake }

func (w *memWriter) WriteMemoraContext(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	w.mem.set("session:"+sessionID, string(payload))
	return nil
}

// countingWriter 复用 prewarm_test.go 的 atomic 计数形态（E2E 等待用）。
type countingWriter struct {
	inner RedisSideWriter
	calls int32
}

func (w *countingWriter) WriteMemoraContext(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	atomic.AddInt32(&w.calls, 1)
	return w.inner.WriteMemoraContext(ctx, sessionID, payload, ttl)
}

type memReader struct{ mem *memFake }

func (r *memReader) ReadMemoraContext(ctx context.Context, sessionID string) (string, error) {
	if v := r.mem.get("session:" + sessionID); v != "" {
		return v, nil
	}
	return "", errNotFound
}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var errNotFound = notFoundErr{}
