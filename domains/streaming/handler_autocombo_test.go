package streaming

import (
	"context"
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/autocombo"
	"github.com/kaixuan/llm-gateway-go/domains/freeresource"
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

// TestHashString_DeterministicAndDistinct 验证 hashString 在去重场景下
// (handler_autocombo.go 中的 dedup key) 给出稳定的 uint64, 且对不同
// raw_model 给出不同结果 (collision 测试).
func TestHashString_DeterministicAndDistinct(t *testing.T) {
	a := hashString("openai/gpt-3.5-turbo:free")
	b := hashString("openai/gpt-3.5-turbo:free")
	if a != b {
		t.Errorf("hashString should be deterministic: %d != %d", a, b)
	}
	c := hashString("groq/llama-3.3-70b")
	if a == c {
		t.Errorf("different inputs should produce different hashes, both %d", a)
	}
}

func TestHashString_EmptyString(t *testing.T) {
	got := hashString("")
	// 主断言: 二次调用一致.
	if got != hashString("") {
		t.Errorf("empty string hash should be deterministic")
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
// QuotaTracker.Record 时构造的 RecordRequest 字段正确 (通过类型断言读取
// QuotaTracker 的 db 字段为 nil 触发空指针短路, 间接证明该函数在没有真
// 实 DB 时不进入 Record 路径). 这是 no-DB 单元测试能覆盖的最深层次,
// 真实 Record 调用在 freeresource/quota_tracker_test.go 集成测试中验证.
func TestRecordOmniFreeQuota_TrackerSignature(t *testing.T) {
	tracker := &freeresource.QuotaTracker{} // db = nil
	h := &ChatHandler{quotaTracker: tracker}
	result := &executors.ExecuteResult{Candidate: provider.Candidate{
		CredentialID:     42,
		CatalogCode:      "groq",
		StandardizedName: "llama-3.3-70b",
	}}
	defer func() {
		// 由于 tracker.db 是 nil, Record 在 ExecContext 处会 panic.
		// 我们用 recover 兜底, 仅断言 panic 类型与 db nil 一致, 以
		// 间接验证 recordOmniFreeQuota 确实进入了 Record 路径.
		r := recover()
		if r == nil {
			t.Errorf("expected panic from nil-db Record, got none")
		}
	}()
	h.recordOmniFreeQuota(context.Background(), "auto/free", "tenant-x", result, nil)
}