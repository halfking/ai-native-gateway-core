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

// ── 2026-11-15 三点审计轮 ─────────────────────────────────────────────

// TestKeyConcurrentGate_NonPositiveIDNeverBuckets 钉点①（key 标识唯一性）：
// ID<=0 的身份**绝不落桶**——used[0] 会把所有无效身份聚成一个共享桶互相
// 挤占（不同 key 误拒）。宁 bypass 不共享桶。生产 Verify 链 ID 恒>0，
// 命中即装配/数据异常（WARN 可观测）。
// 变异臂：删掉 ID<=0 守卫 ⇒ 第二段断言红（used 出现 0 桶）。
func TestKeyConcurrentGate_NonPositiveIDNeverBuckets(t *testing.T) {
	gate := NewKeyConcurrentGate()
	cap1 := 1
	h := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 两个不同「身份」但 ID 都是 0：若落共享桶，第二个必 429。
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, gatePost("/v1/chat/completions",
			gateCtxWith(&authentication.KeyInfo{ID: 0, TenantID: "t", RateLimitConcurrent: &cap1})))
		if rec.Code != http.StatusOK {
			t.Fatalf("ID<=0 必须 bypass（不得共享桶误拒）: 第 %d 笔 status=%d", i+1, rec.Code)
		}
	}
	// 负 ID 同族。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gatePost("/v1/chat/completions",
		gateCtxWith(&authentication.KeyInfo{ID: -5, TenantID: "t", RateLimitConcurrent: &cap1})))
	if rec.Code != http.StatusOK {
		t.Fatalf("负 ID 必须 bypass: status=%d", rec.Code)
	}

	// 采样面同样不得出现 key<=0 的样本（守卫在落桶之前）。
	for _, s := range gate.SnapshotAndResetMinute() {
		if s.KeyID <= 0 {
			t.Fatalf("采样面不得出现非正 key id: %+v", s)
		}
	}
}

// fakeGateVerifier 是身份回退解析的最小桩（点②）。
type fakeGateVerifier struct {
	calls int
	info  *authentication.KeyInfo
	err   error
}

func (f *fakeGateVerifier) Verify(ctx context.Context, rawKey string) (*authentication.KeyInfo, error) {
	f.calls++
	return f.info, f.err
}

// TestKeyConcurrentGate_VerifierFallbackResolvesIdentity 钉点②（身份回退）：
// embeddings/audio/images 等面不写 context memo——无回退时闸读不到身份
// 空转；SetVerifier 装配后必须能按 Bearer token 自行解析并执法。
// 同一定时也钉「context 身份优先，verifier 不被调用」。
func TestKeyConcurrentGate_VerifierFallbackResolvesIdentity(t *testing.T) {
	cap1 := 1
	fallback := &fakeGateVerifier{info: &authentication.KeyInfo{ID: 41, TenantID: "t", RateLimitConcurrent: &cap1}}
	gate := NewKeyConcurrentGate()
	gate.SetVerifier(fallback)
	blocker := &blockingNext{entered: make(chan struct{}, 2), release: make(chan struct{}, 2), done: make(chan struct{}, 2)}
	h := gate.Wrap(blocker)

	// 无 context 身份 + Bearer 头 ⇒ 回退解析 ⇒ 执法帽=1。
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req.Header.Set("Authorization", "Bearer sk-fallback-test")
	go h.ServeHTTP(httptest.NewRecorder(), req)
	<-blocker.entered // 槽占住（证明回退身份生效）

	rec := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req2.Header.Set("Authorization", "Bearer sk-fallback-test")
	h.ServeHTTP(rec, req2)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("回退身份必须被执法（帽=1，第 2 笔 429）: got %d", rec.Code)
	}
	blocker.release <- struct{}{}
	<-blocker.done
	if fallback.calls == 0 {
		t.Fatal("无 context 身份时必须走 verifier 回退")
	}

	// context 身份在场 ⇒ 优先，不再碰 verifier（同 key 41 已释放，此笔
	// 拿到槽进入 blocker——需编排放行）。
	before := fallback.calls
	rec3 := httptest.NewRecorder()
	rec3Done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec3, gatePost("/v1/chat/completions", gateCtx(41, 1)))
		close(rec3Done)
	}()
	<-blocker.entered
	if fallback.calls != before {
		t.Fatalf("context 身份在场时不得调用回退 verifier（want %d calls, got %d）", before, fallback.calls)
	}
	blocker.release <- struct{}{}
	<-blocker.done
	<-rec3Done
	if rec3.Code != http.StatusOK {
		t.Fatalf("context 身份路径必须照常放行第 1 笔: %d", rec3.Code)
	}
}

// TestKeyConcurrentGate_VerifierFallbackFailureBypasses 钉回退失败语义：
// Verify 出错/无 Bearer = 无身份 = 豁免直通（无效 key 由各 handler 自己的
// 鉴权拒绝，闸不越权报错）。
func TestKeyConcurrentGate_VerifierFallbackFailureBypasses(t *testing.T) {
	gate := NewKeyConcurrentGate()
	gate.SetVerifier(&fakeGateVerifier{info: nil, err: context.Canceled})
	h := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	// Verify 失败。
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req.Header.Set("Authorization", "Bearer sk-bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("回退失败必须直通: %d", rec.Code)
	}
	// 无 Bearer 头。
	req2 := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("无 Bearer 必须直通: %d", rec2.Code)
	}
}

// TestKeyConcurrentGate_MinuteStatsAccounting 钉点③（分钟观测面）：
// SnapshotAndResetMinute 必须报出窗口内每个有活动 key 的
// {InFlight, PeakInFlight, Rejected, Limit}，且窗口累计量被清零、
// in_flight 是实时值不被清零。
func TestKeyConcurrentGate_MinuteStatsAccounting(t *testing.T) {
	gate := NewKeyConcurrentGate()
	cap1 := 1
	blocker := &blockingNext{entered: make(chan struct{}, 4), release: make(chan struct{}, 4), done: make(chan struct{}, 4)}
	h := gate.Wrap(blocker)

	// key 41：占住 1 槽（帽=1），再打 2 笔被拒。
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req.Header.Set("Authorization", "Bearer sk-x")
	gate.SetVerifier(&fakeGateVerifier{info: &authentication.KeyInfo{ID: 41, TenantID: "t", RateLimitConcurrent: &cap1}})
	go h.ServeHTTP(httptest.NewRecorder(), req)
	<-blocker.entered
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		rj := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
		rj.Header.Set("Authorization", "Bearer sk-x")
		h.ServeHTTP(rec, rj)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("预置拒绝: got %d", rec.Code)
		}
	}

	// key 42（context 身份，帽=2）：两笔并发（peak=2）。
	ctx42 := gateCtx(42, 2)
	go h.ServeHTTP(httptest.NewRecorder(), gatePost("/v1/chat/completions", ctx42))
	<-blocker.entered
	go h.ServeHTTP(httptest.NewRecorder(), gatePost("/v1/chat/completions", ctx42))
	<-blocker.entered

	samples := gate.SnapshotAndResetMinute()
	byKey := map[int]KeyConcurrencySample{}
	for _, s := range samples {
		byKey[s.KeyID] = s
	}
	s41, ok := byKey[41]
	if !ok {
		t.Fatalf("key 41 必须有样本: %+v", samples)
	}
	if s41.InFlight != 1 || s41.PeakInFlight != 1 || s41.Rejected != 2 || s41.Limit != 1 {
		t.Fatalf("key 41 样本口径错: %+v", s41)
	}
	s42, ok := byKey[42]
	if !ok {
		t.Fatalf("key 42 必须有样本: %+v", samples)
	}
	if s42.InFlight != 2 || s42.PeakInFlight != 2 || s42.Rejected != 0 || s42.Limit != 2 {
		t.Fatalf("key 42 样本口径错（peak 必须捕捉并发峰）: %+v", s42)
	}

	// 窗口重置：再次采样只剩仍在飞的 key，且 Rejected 已清零。
	second := gate.SnapshotAndResetMinute()
	byKey2 := map[int]KeyConcurrencySample{}
	for _, s := range second {
		byKey2[s.KeyID] = s
	}
	if _, still := byKey2[41]; !still {
		t.Fatal("key 41 仍在飞（1 槽未释放），第二窗口必须有样本")
	}
	if byKey2[41].InFlight != 1 {
		t.Fatalf("in_flight 是实时值不被窗口清零: %+v", byKey2[41])
	}
	if byKey2[41].Rejected != 0 {
		t.Fatalf("窗口累计量必须清零: %+v", byKey2[41])
	}
	if byKey2[41].PeakInFlight != 1 {
		t.Fatalf("新窗口 peak 从当前在飞继续累计: %+v", byKey2[41])
	}

	// 收尾放行。
	blocker.release <- struct{}{}
	blocker.release <- struct{}{}
	blocker.release <- struct{}{}
	<-blocker.done
	<-blocker.done
	<-blocker.done

	// 第三窗口：窗口开始时刻两 key 仍在飞（占用真实存在过）⇒ 有样本，
	// InFlight=0（已释放）但 Peak≥窗口开头占用（truthful 下界）。
	// 批判式审计轮补：Limit 必须同步播种（否则 peak>0 而 limit=0 自相矛盾）。
	third := gate.SnapshotAndResetMinute()
	for _, s := range third {
		if s.InFlight != 0 {
			t.Fatalf("已全部释放，in_flight 必须为 0: %+v", s)
		}
		if s.PeakInFlight < 1 {
			t.Fatalf("窗口开头仍在飞的 key，peak 必须反映该占用: %+v", s)
		}
		if s.Limit <= 0 {
			t.Fatalf("跨窗样本 Limit 必须播种（peak>0 意味着有帽在执法）: %+v", s)
		}
	}

	// 真正的空窗口（无在飞、无播种、无拒绝）：零样本。
	if got := gate.SnapshotAndResetMinute(); len(got) != 0 {
		t.Fatalf("无活动窗口必须零样本: %+v", got)
	}
}
