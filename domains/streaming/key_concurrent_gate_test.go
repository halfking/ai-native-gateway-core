package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

func gateCtx(id, concurrent int) context.Context {
	info := &authentication.KeyInfo{ID: id, TenantID: "tenant-gate", RateLimitConcurrent: &concurrent}
	return WithVerifiedKeyInfoForTest(context.Background(), info)
}

func gateCtxWith(info *authentication.KeyInfo) context.Context {
	return WithVerifiedKeyInfoForTest(context.Background(), info)
}

func gatePost(path string, ctx context.Context) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	return r.WithContext(ctx)
}

// blockingNext 下游桩：进入即通知（entered），随后阻塞等待放行（release），
// 完成后通知（done）——精确控制并发窗口时序。
type blockingNext struct {
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (h *blockingNext) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.entered <- struct{}{}
	<-h.release
	w.WriteHeader(http.StatusOK)
	h.done <- struct{}{}
}

// TestKeyConcurrentGate_RejectsBeyondCap 钉并发帽真执法（2026-11-15）：帽 N
// 时第 N+1 笔必须 429 标准信封——旧形态（executors TryAcquire 失败仅告警
// 放行）下配置是死的。槽随请求结束回落（defer release）。
func TestKeyConcurrentGate_RejectsBeyondCap(t *testing.T) {
	gate := NewKeyConcurrentGate()
	blocker := &blockingNext{entered: make(chan struct{}, 4), release: make(chan struct{}, 4), done: make(chan struct{}, 4)}
	h := gate.Wrap(blocker)
	ctx := gateCtx(11, 2) // 帽 = 2

	done := make(chan int, 4)
	go func() { h.ServeHTTP(httptest.NewRecorder(), gatePost("/v1/chat/completions", ctx)); done <- 1 }()
	<-blocker.entered // 槽 1 占住
	go func() { h.ServeHTTP(httptest.NewRecorder(), gatePost("/v1/chat/completions", ctx)); done <- 2 }()
	<-blocker.entered // 槽 2 占住

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gatePost("/v1/chat/completions", ctx))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 笔必须 429（帽=2）: got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("429 body 必须用统一 code: %s", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 必须带 Retry-After")
	}

	// 放行在飞的两笔，槽回落。
	blocker.release <- struct{}{}
	<-blocker.done
	blocker.release <- struct{}{}
	<-blocker.done
	<-done
	<-done

	// 槽回落后的新请求必须放行（goroutine 编排：下游桩需要一次 release）。
	recProbe := httptest.NewRecorder()
	probeDone := make(chan int, 1)
	go func() { h.ServeHTTP(recProbe, gatePost("/v1/chat/completions", ctx)); probeDone <- 1 }()
	<-blocker.entered
	blocker.release <- struct{}{}
	<-probeDone
	if recProbe.Code != http.StatusOK {
		t.Fatalf("槽回落后的新请求必须放行: got %d", recProbe.Code)
	}
}

// TestKeyConcurrentGate_SkipsUnlimitedAndInternal 钉豁免面：显式 0（无限）、
// 无 key 身份（静态 key 旁路/内部回环）、IsInternal key 全部直通。
func TestKeyConcurrentGate_SkipsUnlimitedAndInternal(t *testing.T) {
	gate := NewKeyConcurrentGate()
	var hits int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	h := gate.Wrap(next)

	zero := 0
	unlimited := &authentication.KeyInfo{ID: 12, TenantID: "t", RateLimitConcurrent: &zero}
	internal := &authentication.KeyInfo{ID: 13, TenantID: "t", RateLimitConcurrent: &zero, IsInternal: true}
	for i := 0; i < 5; i++ {
		for _, ctx := range []context.Context{
			gateCtxWith(unlimited),
			context.Background(), // 无身份
			gateCtxWith(internal),
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, gatePost("/v1/messages", ctx))
			if rec.Code != http.StatusOK {
				t.Fatalf("豁免面不得被拦: status=%d", rec.Code)
			}
		}
	}
	if hits != 15 {
		t.Fatalf("豁免面全部应到达下游（want 15, got %d）", hits)
	}
}

// TestKeyConcurrentGate_PerKeyIsolation 钉分桶：key A 满槽不影响 key B。
func TestKeyConcurrentGate_PerKeyIsolation(t *testing.T) {
	gate := NewKeyConcurrentGate()
	blocker := &blockingNext{entered: make(chan struct{}, 4), release: make(chan struct{}, 4), done: make(chan struct{}, 4)}
	h := gate.Wrap(blocker)
	ctxA := gateCtx(21, 1)

	done := make(chan int, 2)
	go func() { h.ServeHTTP(httptest.NewRecorder(), gatePost("/v1/chat/completions", ctxA)); done <- 1 }()
	<-blocker.entered // key A 占住槽

	// key B 走 goroutine 编排（下游桩需要一次 release 才能完成）。
	recB := httptest.NewRecorder()
	bDone := make(chan int, 1)
	go func() { h.ServeHTTP(recB, gatePost("/v1/chat/completions", gateCtx(22, 1))); bDone <- 1 }()
	<-blocker.entered // key B 也进入下游桩（此信号即「未被 key A 污染」的证词）
	blocker.release <- struct{}{}
	blocker.release <- struct{}{} // A 与 B 各一个 token
	<-blocker.done
	<-blocker.done
	<-bDone
	<-done
	if recB.Code != http.StatusOK {
		t.Fatalf("key B 不得被 key A 的满槽污染: %d", recB.Code)
	}
}

// TestKeyConcurrentGate_WrapPerRoute — 2026-11-15 S09 下钻轮钉测：同一个
// gate 实例对三个路由各调一次 Wrap，三个路由必须各自命中自己的 handler
// （首版实现把 next 存进实例字段 ⇒ 最后一次 Wrap 覆盖前两次 ⇒ chat 面
// 全量反转为 responses 形状——245 mock 栈 A/B 三方对拍实证）。
// 变异臂：把 Wrap 退回 g.next 字段形态 ⇒ 本测试红。
func TestKeyConcurrentGate_WrapPerRoute(t *testing.T) {
	gate := NewKeyConcurrentGate()
	var chatHit, messagesHit, responsesHit bool
	chatHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatHit = true })
	messagesHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { messagesHit = true })
	responsesHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { responsesHit = true })

	chatWrapped := gate.Wrap(chatHandler)
	messagesWrapped := gate.Wrap(messagesHandler)
	responsesWrapped := gate.Wrap(responsesHandler)

	// 无 key 身份走豁免直通（各自 next 必须被命中）。
	for _, tc := range []struct {
		name string
		h    http.Handler
		want *bool
	}{
		{"chat", chatWrapped, &chatHit},
		{"messages", messagesWrapped, &messagesHit},
		{"responses", responsesWrapped, &responsesHit},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/"+tc.name, nil)
		rec := httptest.NewRecorder()
		tc.h.ServeHTTP(rec, req)
		if !*tc.want {
			t.Fatalf("%s route did not reach its own handler — shared-gate next shadowing regression", tc.name)
		}
	}
}

// TestKeyConcurrentGate_NullColumnMeansNoCap 钉 2026-11-15 修正（211 生产
// 反馈轮）：并发帽 explicit-only——RateLimitConcurrent=NULL 的 key（tier
// default 本会回退到 6）**不设帽**：存量 82 个 active key 的 NULL 列在执法
// 接入前从未代表「6 并发」，接入即按 tier 默认帽住全体 = 行为突变。
// 变异臂：serve 里改回 EffectiveConcurrent() ⇒ 本测试红。
func TestKeyConcurrentGate_NullColumnMeansNoCap(t *testing.T) {
	gate := NewKeyConcurrentGate()
	// NULL 列 + default 档（tier 默认并发=6）——若落 tier 默认，第 7 笔必拦。
	blocker := &blockingNext{entered: make(chan struct{}, 16), release: make(chan struct{}, 16), done: make(chan struct{}, 16)}
	h := gate.Wrap(blocker)
	ctx := gateCtxWith(&authentication.KeyInfo{ID: 31, TenantID: "t", KeyTier: "default"}) // RateLimitConcurrent=nil

	const n = 10 // > tier 默认 6
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() { h.ServeHTTP(httptest.NewRecorder(), gatePost("/v1/chat/completions", ctx)); done <- 1 }()
	}
	// 全部 10 笔都应进入下游（无人被 429）。
	for i := 0; i < n; i++ {
		<-blocker.entered
	}
	for i := 0; i < n; i++ {
		blocker.release <- struct{}{}
	}
	for i := 0; i < n; i++ {
		<-done
	}
}
