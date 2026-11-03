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

	"github.com/prometheus/client_golang/prometheus/testutil"
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

// panicClient：Prewarm 一进来就 panic（模拟预热链路任意未预期 panic）。
type panicClient struct {
	calls atomic.Int32
}

func (f *panicClient) Prewarm(ctx context.Context, tenantID, sessionID, query string) ([]byte, bool) {
	f.calls.Add(1)
	panic("boom: prewarm exploded")
}

// 7) panic 路径补记 unavailable 终态（F-3：recover 不能只写日志）。
//
// ★ 修前：recover 分支只写日志 ⇒ panic 的那一轮在漏斗里悬空——requested
//
//	已计、四终态都没记 ⇒ 求和永远对不上（漏斗泄漏一格）。
func TestPanicRecordsUnavailableTerminalState(t *testing.T) {
	beforeUnavailable := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeUnavailable))
	beforeHit := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeHit))

	c := &panicClient{}
	w := &fakeWriter{}
	Trigger(c, w, nil, nil, "t", "sess-panic-terminal", "prompt", time.Hour)
	waitFor(t, func() bool { return c.calls.Load() == 1 })
	waitFor(t, func() bool {
		return testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeUnavailable)) > beforeUnavailable
	})

	if got := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeHit)); got != beforeHit {
		t.Fatalf("panic must not be counted as hit, hit %v -> %v", beforeHit, got)
	}
	if w.calls.Load() != 0 {
		t.Fatalf("panicked prewarm must not side-write, writes=%d", w.calls.Load())
	}
}

// 8) 漏斗求和闭合（F-3 同族补强）：一轮完整 Trigger（成功 / 写失败 /
// HTTP 失败 / panic 四路）⇒ hit+miss+timeout+unavailable 增量之和
// **恰好等于** requested 增量。
//
// ★ 这是五态口径的闭合门：任何一条「requested 已计、终态没记」的路径
//
//	（panic 只写日志是第一例）都会让求和差一 ⇒ 本门红。
func TestFunnelTerminalStatesSumToRequested(t *testing.T) {
	snap := func() (req, hit, miss, to, un float64) {
		return testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeRequested)),
			testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeHit)),
			testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeMiss)),
			testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeTimeout)),
			testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeUnavailable))
	}
	req0, hit0, miss0, to0, un0 := snap()

	// ① 成功：HTTP 有结果 + 旁路写成功 ⇒ hit。
	Trigger(&fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)},
		&fakeWriter{}, nil, nil, "t", "sess-funnel-hit", "prompt", time.Hour)
	// ② 写失败（非超时）⇒ unavailable（ClassifyWriteError 口径）。
	Trigger(&fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)},
		&fakeWriter{err: errors.New("redis down")}, nil, nil, "t", "sess-funnel-writefail", "prompt", time.Hour)
	// ③ HTTP 失败：真 PrewarmHTTPClient 打 204 ⇒ miss（生产客户端的非 200
	//    记态分支：RecordPrewarm(ClassifyOutcome(status, nil))）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	httpc := &PrewarmHTTPClient{baseURL: srv.URL, token: "x",
		httpClient: &http.Client{Timeout: DefaultHTTPTimeout}}
	Trigger(httpc, &fakeWriter{}, nil, nil, "t", "sess-funnel-http", "prompt", time.Hour)
	// ④ panic ⇒ unavailable（F-3 修复的 recover 记态分支）。
	Trigger(&panicClient{}, &fakeWriter{}, nil, nil, "t", "sess-funnel-panic", "prompt", time.Hour)

	// 等四个终态全部落地（四条路径各记恰好一态 ⇒ 终态增量之和到 4）。
	waitFor(t, func() bool {
		_, hit, miss, to, un := snap()
		return (hit-hit0)+(miss-miss0)+(to-to0)+(un-un0) >= 4
	})
	time.Sleep(80 * time.Millisecond) // 排干可能的迟到 goroutine

	req1, hit1, miss1, to1, un1 := snap()
	dReq := req1 - req0
	dHit, dMiss, dTo, dUn := hit1-hit0, miss1-miss0, to1-to0, un1-un0
	if dReq != 4 {
		t.Fatalf("requested delta = %v, want 4 (one per path)", dReq)
	}
	// 逐路归位：hit+1（成功）/ miss+1（HTTP 204）/ unavailable+2（写失败＋
	// panic）/ timeout+0。
	if dHit != 1 || dMiss != 1 || dUn != 2 || dTo != 0 {
		t.Fatalf("per-path states drifted: hit=%v miss=%v timeout=%v unavailable=%v", dHit, dMiss, dTo, dUn)
	}
	if sum := dHit + dMiss + dTo + dUn; sum != dReq {
		t.Fatalf("funnel not closed: hit+miss+timeout+unavailable = %v != requested = %v", sum, dReq)
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
