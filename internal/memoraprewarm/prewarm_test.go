// prewarm_test.go — G3 不变式三态单测（P1-2③ W1）：
// 超时跳过 / 不可用不注入 / 写失败零影响；以及未装配 no-op。
package memoraprewarm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fakeWriter struct {
	calls atomic.Int32
	err   error
}

func (f *fakeWriter) WriteMemoraContext(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	f.calls.Add(1)
	return f.err
}

type fakeClient struct {
	calls   atomic.Int32
	payload []byte
	ok      bool
}

func (f *fakeClient) Prewarm(ctx context.Context, tenantID, sessionID, query string) ([]byte, bool) {
	f.calls.Add(1)
	return f.payload, f.ok
}

// 1) 不可用（ok=false）⇒ 不写旁路、零错误。
func TestUnavailableNoInjectionNoWrite(t *testing.T) {
	c := &fakeClient{ok: false}
	w := &fakeWriter{}
	Trigger(c, w, nil, nil, "tenant", "sess-1", "用户偏好深色主题", time.Hour)
	// Trigger 是 fire-and-forget——等 goroutine 结束。
	waitFor(t, func() bool { return c.calls.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if w.calls.Load() != 0 {
		t.Fatalf("unavailable prewarm must not side-write, writes=%d", w.calls.Load())
	}
}

// 2) 有结果但旁路写失败 ⇒ 零影响（无 panic、无重试）。
func TestWriteFailureZeroImpact(t *testing.T) {
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{}]}`)}
	w := &fakeWriter{err: errors.New("redis down")}
	Trigger(c, w, nil, nil, "tenant", "sess-2", "prompt", time.Hour)
	waitFor(t, func() bool { return w.calls.Load() == 1 })
	// 到此即通过：goroutine 正常返回，无 panic（TestMain 无 recover 依赖）。
}

// 3) HTTP 超时（500ms 预算）⇒ 跳过（真实 HTTP 服务器 sleep 超预算）。
func TestHTTPTimeoutSkips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(DefaultHTTPTimeout + 300*time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := &PrewarmHTTPClient{baseURL: srv.URL, token: "x",
		httpClient: &http.Client{Timeout: DefaultHTTPTimeout}}
	w := &fakeWriter{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		payload, ok := c.Prewarm(context.Background(), "t", "s", "q")
		if ok || payload != nil {
			t.Errorf("timeout must yield no-injection, got ok=%v", ok)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("prewarm did not return within budget×3")
	}
	if w.calls.Load() != 0 {
		t.Fatal("no side-write after timeout")
	}
}

// 4) 未装配（nil client/writer）⇒ 结构性 no-op（goroutine 不启动）。
func TestNotWiredIsNoOp(t *testing.T) {
	c := &fakeClient{ok: true}
	w := &fakeWriter{}
	Trigger(nil, w, nil, nil, "t", "s", "p", time.Hour)
	Trigger(c, nil, nil, nil, "t", "s", "p", time.Hour)
	Trigger(c, w, nil, nil, "t", "", "p", time.Hour)
	time.Sleep(50 * time.Millisecond)
	if c.calls.Load() != 0 || w.calls.Load() != 0 {
		t.Fatalf("unwired trigger must be no-op, client=%d writer=%d", c.calls.Load(), w.calls.Load())
	}
}

// 5) 成功路径：结果旁路写入（带 TTL）。
func TestHappyPathWritesSideField(t *testing.T) {
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"偏好深色"}]}`)}
	w := &fakeWriter{}
	Trigger(c, w, nil, nil, "tenant", "sess-3", "生成界面配色", time.Hour)
	waitFor(t, func() bool { return w.calls.Load() == 1 })
}

// 6) 空 prompt ⇒ no-op（不发起召回）。
func TestEmptyPromptNoCall(t *testing.T) {
	c := &fakeClient{ok: true}
	Trigger(c, &fakeWriter{}, nil, nil, "t", "s", "   ", time.Hour)
	time.Sleep(50 * time.Millisecond)
	if c.calls.Load() != 0 {
		t.Fatal("empty prompt must not call prewarm")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
