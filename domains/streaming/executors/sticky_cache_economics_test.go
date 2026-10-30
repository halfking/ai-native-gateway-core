package executors

// sticky_cache_economics_test.go — C1（sticky 缓存经济学）的门。
//
// 判据（研究方案 §五 C1 验收量具）：
//	① 大 cache（≥1024 tokens）续期不变（true）；
//	② 小 cache 到期漂移（<1024 且已粘过半 ⇒ false = 不续期）；
//	③ 无 usage 上报时零变化（fail-open ⇒ true）；
//	④ 小 cache 但未过半 ⇒ 仍续期（true —— 会话还年轻）。
//
// 另含 GetL1BindingAge 访问器的直接断言（含过期/异凭据 fail-open 面）。

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func intP(v int) *int { return &v }

func econParams(model string, cacheRead, cacheWrite int, capture *audit.StreamCapture) *ExecParams {
	if capture == nil && (cacheRead > 0 || cacheWrite > 0) {
		capture = audit.NewStreamCapture()
		capture.ObserveUsage(intP(0), intP(0), intP(cacheRead), intP(cacheWrite))
	}
	return &ExecParams{
		Model:     model,
		SessionID: "sess-econ",
		TenantID:  "t1",
		Capture:   capture,
	}
}

func econSticky(t *testing.T, model string, credID int, age time.Duration) (*StickyCache, string) {
	t.Helper()
	prev := stickyTTLConfig
	setStickyTTLConfigForTest(settings.DefaultStickyTTLConfig())
	t.Cleanup(func() { setStickyTTLConfigForTest(prev) })
	ttl := calculateSessionStickyTTL(model)

	s := NewStickyCache()
	l1, _, _ := buildStickyKeys("t1", nil, nil, "", "sess-econ", model)
	if l1 == "" {
		t.Fatalf("l1 key empty for model %s", model)
	}
	// 直接塞一个「已粘 age」的 L1 条目（expiresAt = now + ttl - age）。
	s.mu.Lock()
	s.items[l1] = stickyEntry{
		credentialID: credID,
		expiresAt:    time.Now().Add(ttl - age),
	}
	s.mu.Unlock()
	return s, l1
}

// 判据①：大 cache 续期不变——即便已粘远超 TTL（"过半"也不放走大缓存会话）。
func TestCacheWorthLargeCacheRenews(t *testing.T) {
	model := "gpt-4-turbo" // L1 TTL 10min 档
	sticky, _ := econSticky(t, model, 42, 9*time.Minute)
	p := econParams(model, 800, 300, nil) // 1100 ≥ 1024
	if !stickyCacheWorthRenewal(p, sticky, 42) {
		t.Fatal("large cache must renew (worth ≥ 1024)")
	}
}

// 判据②：小 cache 且已粘过半 ⇒ 不续期（到期漂移）。
func TestCacheColdSmallCacheHalfAgeDrops(t *testing.T) {
	model := "gpt-4-turbo" // TTL 10min；过半 = ≥5min
	sticky, _ := econSticky(t, model, 42, 6*time.Minute)
	p := econParams(model, 100, 0, nil) // 100 < 1024
	if stickyCacheWorthRenewal(p, sticky, 42) {
		t.Fatal("small cache past half age must NOT renew (cacheCold)")
	}
}

// 判据④：小 cache 但未过半 ⇒ 仍续期。
func TestCacheWorthSmallCacheYoungRenews(t *testing.T) {
	model := "gpt-4-turbo"
	sticky, _ := econSticky(t, model, 42, 2*time.Minute)
	p := econParams(model, 100, 0, nil)
	if !stickyCacheWorthRenewal(p, sticky, 42) {
		t.Fatal("small cache before half age must renew (session still young)")
	}
}

// 判据③a：无 usage 上报（cache 字段缺失）⇒ fail-open 续期。
func TestCacheWorthNoUsageFailOpen(t *testing.T) {
	model := "gpt-4-turbo"
	sticky, _ := econSticky(t, model, 42, 9*time.Minute)
	// capture 无 cache 字段（只报了 prompt/completion）。
	cap := audit.NewStreamCapture()
	cap.ObserveUsage(intP(500), intP(50), nil, nil)
	p := econParams(model, 0, 0, cap)
	if !stickyCacheWorthRenewal(p, sticky, 42) {
		t.Fatal("missing cache usage must fail-open to renewal")
	}
	// Capture 整个 nil ⇒ fail-open。
	p2 := econParams(model, 0, 0, audit.NewStreamCapture())
	p2.Capture = nil
	if !stickyCacheWorthRenewal(p2, sticky, 42) {
		t.Fatal("nil capture must fail-open to renewal")
	}
	// 查不到绑定年龄（粘的是别的凭据）⇒ fail-open。
	sticky2, _ := econSticky(t, model, 99, 9*time.Minute)
	p3 := econParams(model, 100, 0, nil)
	if !stickyCacheWorthRenewal(p3, sticky2, 42) {
		t.Fatal("binding for another credential must fail-open to renewal")
	}
}

// GetL1BindingAge 访问器：found/异凭据/过期三面。
func TestGetL1BindingAgeAccessor(t *testing.T) {
	model := "gpt-4-turbo"
	sticky, l1 := econSticky(t, model, 42, 3*time.Minute)
	age, ok := sticky.GetL1BindingAge(l1, model, 42)
	if !ok {
		t.Fatal("binding must be found")
	}
	if age < 2*time.Minute+50*time.Second || age > 3*time.Minute+10*time.Second {
		t.Fatalf("age = %s want ~3m", age)
	}
	// 异凭据 ⇒ not found。
	if _, ok := sticky.GetL1BindingAge(l1, model, 43); ok {
		t.Fatal("other credential must not be found")
	}
	// 过期 ⇒ not found。
	sticky.mu.Lock()
	sticky.items[l1] = stickyEntry{credentialID: 42, expiresAt: time.Now().Add(-time.Minute)}
	sticky.mu.Unlock()
	if _, ok := sticky.GetL1BindingAge(l1, model, 42); ok {
		t.Fatal("expired binding must not be found")
	}
	// nil 接收者安全。
	var nilSticky *StickyCache
	if _, ok := nilSticky.GetL1BindingAge(l1, model, 42); ok {
		t.Fatal("nil sticky must not be found")
	}
}
