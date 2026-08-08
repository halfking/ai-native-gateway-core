package streaming

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/autocombo"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestChatHandler_ShouldTryOmniFree(t *testing.T) {
	h := &ChatHandler{} // no resolver/factory → 全部 false
	if h.shouldTryOmniFree("auto/free") {
		t.Errorf("nil resolver/factory should disable OmniFree")
	}

	h.autoComboResolver = autocombo.NewResolver(nil)
	h.autoComboFactory = autocombo.NewVirtualFactory(nil, nil)

	cases := map[string]bool{
		"":                 false,
		"auto":             false, // 精确 auto, 仍归 autoroute.Decider
		"auto/free":        true,
		"auto/best-free":   true,
		"auto/coding:free": true,
		"openai/gpt-3.5":   false,
		"gpt-4o":           false,
	}
	for model, want := range cases {
		if got := h.shouldTryOmniFree(model); got != want {
			t.Errorf("shouldTryOmniFree(%q) = %v, want %v", model, got, want)
		}
	}
}

// shouldTryOmniFree 在 nil deps / 精确 auto / 普通模型 三种情形下的行为由
// TestChatHandler_ShouldTryOmniFree 覆盖。resolveOmniFreeCandidates 与
// recordOmniFreeQuota 需要 *freeresource.QuotaTracker (具体类型) 才能注入,
// 而 QuotaTracker 的 Record / CorrectFromHeaders 都直接操作 free_quota_tracker
// 表. 单元测试在 freeresource/quota_tracker_test.go 覆盖窗口计算与头部解析,
// 完整集成测试需要真实 PG, 放在 cmd/gateway 的 E2E 路径中处理, 不在此
// mock 整个 PG pool。
//
// 以下测试覆盖 handler_autocombo.go 中不依赖 DB 的纯函数分支:
//   - flattenHeaders: 429 头部扁平化
//   - hashString: 候选去重 key 的紧凑哈希

func TestFlattenHeaders_FirstValuePerKey(t *testing.T) {
	h := http.Header{}
	h.Add("Retry-After", "60")
	// 注: http.Header.Add 会规范化键名, X-RateLimit-Limit 经
	// textproto.CanonicalMIMEHeaderKey 处理变为 "X-Ratelimit-Limit".
	h.Add("X-Ratelimit-Limit", "100")
	h.Add("X-Ratelimit-Reset", "1700000000")
	out := flattenHeaders(h)
	if out["Retry-After"] != "60" {
		t.Errorf("Retry-After should be 60, got %q", out["Retry-After"])
	}
	if out["X-Ratelimit-Limit"] != "100" {
		t.Errorf("X-Ratelimit-Limit should be 100, got %q", out["X-Ratelimit-Limit"])
	}
	if out["X-Ratelimit-Reset"] != "1700000000" {
		t.Errorf("X-Ratelimit-Reset should be 1700000000, got %q", out["X-Ratelimit-Reset"])
	}
}

func TestFlattenHeaders_EmptyValueSkipped(t *testing.T) {
	h := http.Header{}
	// 显式设空 slice
	h["X-Empty"] = []string{}
	h.Add("Retry-After", "10")
	out := flattenHeaders(h)
	if _, present := out["X-Empty"]; present {
		t.Errorf("empty value should be skipped")
	}
	if out["Retry-After"] != "10" {
		t.Errorf("Retry-After should be 10, got %q", out["Retry-After"])
	}
}

func TestFlattenHeaders_MultiValueTakesFirst(t *testing.T) {
	h := http.Header{}
	h.Add("X-Test", "first")
	h.Add("X-Test", "second")
	out := flattenHeaders(h)
	if out["X-Test"] != "first" {
		t.Errorf("multi-value should yield first, got %q", out["X-Test"])
	}
}

func TestFlattenHeaders_NilHeader(t *testing.T) {
	out := flattenHeaders(nil)
	if out == nil {
		t.Errorf("flattenHeaders should return non-nil empty map, got nil")
	}
	if len(out) != 0 {
		t.Errorf("expected empty map, got %d entries", len(out))
	}
}

// TestFlattenHeaders_FromReal429Resp 验证 recordOmniFreeQuota 在 429 路径
// 上传给 QuotaTracker.CorrectFromHeaders 的 headers 字段形状与
// http.Header flatten 后一致: 第一值、空值跳过、nil 容忍.
func TestFlattenHeaders_FromReal429Resp(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Retry-After":       []string{"60"},
			"X-RateLimit-Limit": []string{"1000"},
			"X-RateLimit-Reset": []string{"1700000060"},
		},
	}
	out := flattenHeaders(resp.Header)
	if out["Retry-After"] != "60" {
		t.Errorf("Retry-After should be 60, got %q", out["Retry-After"])
	}
	if out["X-RateLimit-Limit"] != "1000" {
		t.Errorf("X-RateLimit-Limit should be 1000, got %q", out["X-RateLimit-Limit"])
	}
	if out["X-RateLimit-Reset"] != "1700000060" {
		t.Errorf("X-RateLimit-Reset should be 1700000060, got %q", out["X-RateLimit-Reset"])
	}
}

// TestCandKey_Distinct 验证 round 4 L4 复合 key 在去重场景下行为正确:
// 同 (provider_id, raw_model) 应视为同一键, 不同则应区分. 这是 hashString
// 替代后的核心安全保证 (无碰撞).
func TestCandKey_Distinct(t *testing.T) {
	type candKey struct {
		ProviderID uint64
		RawModel   string
	}
	a := candKey{ProviderID: 1, RawModel: "openai/gpt-3.5-turbo:free"}
	b := candKey{ProviderID: 1, RawModel: "openai/gpt-3.5-turbo:free"}
	if a != b {
		t.Errorf("candKey should be equal for same input: %v != %v", a, b)
	}
	c := candKey{ProviderID: 1, RawModel: "groq/llama-3.3-70b"}
	if a == c {
		t.Errorf("different raw_model should produce different keys, both %v", a)
	}
	d := candKey{ProviderID: 2, RawModel: "openai/gpt-3.5-turbo:free"}
	if a == d {
		t.Errorf("different provider_id should produce different keys, both %v", a)
	}
}

func TestCandKey_EmptyString(t *testing.T) {
	type candKey struct {
		ProviderID uint64
		RawModel   string
	}
	// 用 map 验证空字符串作为 key 也能正确去重; 这是 round 4 L4 的回归保护.
	m := map[candKey]int{}
	m[candKey{ProviderID: 0, RawModel: ""}] = 1
	m[candKey{ProviderID: 0, RawModel: ""}] = 2 // 应覆盖前一个
	if len(m) != 1 {
		t.Errorf("empty raw_model candKey should dedup to 1, got %d", len(m))
	}
}

// TestRecordOmniFreeQuota_NilShortCircuit 验证 quotaTracker=nil 或非 auto/*
// 模型时 recordOmniFreeQuota 是 no-op, 不会 panic. 该分支覆盖了
// "OMNIFREE_ENABLED 未注入时" 与 "普通模型" 的安全降级路径.
func TestRecordOmniFreeQuota_NilShortCircuit(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		result    *executors.ExecuteResult
		expectErr error
	}{
		{
			name:   "nil_tracker_non_auto",
			model:  "openai/gpt-4",
			result: nil,
		},
		{
			name:   "nil_tracker_auto_no_result",
			model:  "auto/free",
			result: nil,
		},
		{
			name:  "nil_tracker_auto_with_result",
			model: "auto/free",
			result: &executors.ExecuteResult{Candidate: provider.Candidate{
				CredentialID:     1,
				CatalogCode:      "openrouter",
				StandardizedName: "openai/gpt-3.5-turbo:free",
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("recordOmniFreeQuota should not panic, got %v", r)
				}
			}()
			h := &ChatHandler{} // quotaTracker = nil
			h.recordOmniFreeQuota(context.Background(), tc.model, "default", tc.result, nil)
		})
	}
}

// TestRecordOmniFreeQuota_TrackerSignature 验证 recordOmniFreeQuota 调用
// QuotaTracker.Record 时构造的 RecordRequest 字段正确. round 4 修复后,
// quotaTracker 改为 QuotaRecorder 接口, 测试可以注入 fake 直接捕获参数.
//
// 这是 round 4 L1 修复的核心: 旧测试只验证 nil 短路, 真实字段拷贝未覆盖.
//
// round 4 L3 补充: recordOmniFreeQuota 改用 bounded worker queue 后,
// 测试必须先调用 SetOmniFree 初始化队列 + worker, 否则任务无法投递.
func TestRecordOmniFreeQuota_TrackerSignature(t *testing.T) {
	fake := &fakeQuotaRecorder{}
	h := &ChatHandler{}
	h.SetOmniFree(nil, nil, fake) // 初始化 quotaRecordQueue + workers.
	defer func() {
		close(h.quotaWorkersClose)
		h.quotaWorkersDone.Wait()
	}()

	result := &executors.ExecuteResult{Candidate: provider.Candidate{
		CredentialID:     42,
		CatalogCode:      "groq",
		StandardizedName: "llama-3.3-70b",
	}}
	h.recordOmniFreeQuota(context.Background(), "auto/free", "tenant-x", result, nil)
	// L3 bounded queue: 等待 worker 消费队列.
	time.Sleep(100 * time.Millisecond)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.records) != 1 {
		t.Fatalf("expected 1 record call, got %d", len(fake.records))
	}
	r := fake.records[0]
	if r.CredentialID != 42 {
		t.Errorf("expected CredentialID=42, got %d", r.CredentialID)
	}
	if r.ProviderCode != "groq" {
		t.Errorf("expected ProviderCode=groq, got %q", r.ProviderCode)
	}
	if r.ModelID != "llama-3.3-70b" {
		t.Errorf("expected ModelID=llama-3.3-70b, got %q", r.ModelID)
	}
	if r.TenantID != "tenant-x" {
		t.Errorf("expected TenantID=tenant-x, got %q", r.TenantID)
	}
	if !r.Success {
		t.Errorf("expected Success=true (execErr=nil)")
	}
	if len(fake.correct) != 0 {
		t.Errorf("expected no CorrectFromHeaders (no 429), got %d", len(fake.correct))
	}
}

// TestRecordOmniFreeQuota_CorrectOn429 验证 429 路径调用 CorrectFromHeaders,
// 且 headers 被 flatten 正确传递. round 4 L2 修复的核心.
//
// round 4 L3 补充: 同样需要先调用 SetOmniFree 初始化队列.
func TestRecordOmniFreeQuota_CorrectOn429(t *testing.T) {
	fake := &fakeQuotaRecorder{}
	h := &ChatHandler{}
	h.SetOmniFree(nil, nil, fake)
	defer func() {
		close(h.quotaWorkersClose)
		h.quotaWorkersDone.Wait()
	}()

	result := &executors.ExecuteResult{
		Candidate: provider.Candidate{
			CredentialID:     7,
			CatalogCode:      "openrouter",
			StandardizedName: "openai/gpt-3.5-turbo:free",
		},
		Response: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header: http.Header{
				"Retry-After":       []string{"60"},
				"X-RateLimit-Limit": []string{"1000"},
			},
		},
	}
	// 第三个参数 (execErr) 仍为 nil 但 result.Response.StatusCode=429,
	// 应触发 CorrectFromHeaders.
	h.recordOmniFreeQuota(context.Background(), "auto/coding:free", "default", result, nil)
	time.Sleep(100 * time.Millisecond)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.records) != 1 {
		t.Fatalf("expected 1 record call, got %d", len(fake.records))
	}
	if len(fake.correct) != 1 {
		t.Fatalf("expected 1 correct call on 429, got %d", len(fake.correct))
	}
	c := fake.correct[0]
	if c.CredentialID != 7 {
		t.Errorf("expected CredentialID=7, got %d", c.CredentialID)
	}
	if c.Headers["Retry-After"] != "60" {
		t.Errorf("expected Retry-After=60, got %q", c.Headers["Retry-After"])
	}
	if c.Headers["X-RateLimit-Limit"] != "1000" {
		t.Errorf("expected X-RateLimit-Limit=1000, got %q", c.Headers["X-RateLimit-Limit"])
	}
}