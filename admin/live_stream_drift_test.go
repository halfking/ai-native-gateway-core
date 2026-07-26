package admin

import (
	"context"
	"testing"
	"time"
)

// TestClassifyModelCategoryFallback_Minimax covers the 2026-07-09 fix for
// 问题3: "minimax-m3" used to fall through to "" because the fallback
// classifier had no "minimax" rule (it only matched "mimo" → xiaomi). The
// vendor (原厂) swim lane then fell back to the model name itself, so the
// provider dimension showed both "MiniMax" (from credential lookup) and
// "minimax-m3" (model-name fallback) as two separate lanes.
func TestClassifyModelCategoryFallback_Minimax(t *testing.T) {
	cases := map[string]string{
		"minimax-m3":           "minimax",
		"MiniMax-M3":           "minimax",
		"minimax-m2.7":         "minimax",
		"minimaxai/minimax-m3": "minimax",
		// mimo must still map to xiaomi and not be shadowed by the new rule.
		"mimo-v2.5-pro": "xiaomi",
	}
	for model, want := range cases {
		if got := classifyModelCategoryFallback(model); got != want {
			t.Errorf("classifyModelCategoryFallback(%q)=%q, want %q", model, got, want)
		}
	}
}

// TestInferVendorFromModel_Minimax mirrors the above for the second vendor
// classifier (live_stream_redis_store.go). Both must agree so the vendor
// dimension never degrades to the model name.
func TestInferVendorFromModel_Minimax(t *testing.T) {
	cases := map[string]string{
		"minimax-m3":    "minimax",
		"MiniMax-M3":    "minimax",
		"mimo-v2.5":     "xiaomi",
		"glm-5.2":       "zhipu",
		"claude-opus-4": "anthropic",
	}
	for model, want := range cases {
		if got := InferVendorFromModel(model); got != want {
			t.Errorf("InferVendorFromModel(%q)=%q, want %q", model, got, want)
		}
	}
}

// TestLiveStreamDimensionKey_ProviderNeverUsesModelName is the core regression
// guard for 问题3: the provider (供应商) dimension must ONLY come from the
// credential → provider reverse lookup (ProviderCode). When ProviderCode is
// empty/unknown, the request must NOT appear in the provider dimension at all
// (empty key) — never the model name. Otherwise the same provider shows up as
// two lanes: the real provider name and a model-name lane.
func TestLiveStreamDimensionKey_ProviderNeverUsesModelName(t *testing.T) {
	// Provider resolved through credential lookup → real provider name.
	got := liveStreamDimensionKey("provider", LiveRequest{
		ProviderCode:  "MiniMax",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "MiniMax" {
		t.Errorf("provider dimension with resolved ProviderCode: got %q, want %q", got, "MiniMax")
	}

	// ProviderCode missing (credential_id==0 / credential deleted): the model
	// name must NOT leak into the provider dimension.
	got = liveStreamDimensionKey("provider", LiveRequest{
		ProviderCode:  "",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "" {
		t.Errorf("provider dimension must be empty when ProviderCode is missing, got %q (model name leaked)", got)
	}

	// Same for the "unknown" sentinel.
	got = liveStreamDimensionKey("provider", LiveRequest{
		ProviderCode:  "unknown",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "" {
		t.Errorf("provider dimension must be empty when ProviderCode is unknown, got %q", got)
	}
}

// TestLiveStreamDimensionKey_ModelStillShowsModelName confirms the model name
// still appears in the MODEL dimension (the fix only stops it leaking into the
// provider dimension).
func TestLiveStreamDimensionKey_ModelStillShowsModelName(t *testing.T) {
	got := liveStreamDimensionKey("model", LiveRequest{
		ProviderCode:  "",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "minimax-m3" {
		t.Errorf("model dimension should still show the model name, got %q", got)
	}
}

// TestLiveRequestFromTelemetry_ModelPrefersCanonicalName 是 2026-07-16 bug 修复
// 的回归测试：实时请求流后台"按模型分维"时，Model 字段应当优先采用标准化
// 模型名（CanonicalName），而不是供应商原始模型名（outbound_model）。原先的
// 顺序是 outbound → client → canonical，导致同名标准模型跨凭证/跨供应商
// 在前端泳道里分裂成多个名字。
//
// 这里通过预填 canonicalCache（避开 DB 依赖）来验证 fallback 链路。
func TestLiveRequestFromTelemetry_ModelPrefersCanonicalName(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	// 预热 canonicalCache: canonicalID=42 → "minimax-m3"
	hub.canonicalCache.Store(42, "minimax-m3")

	cases := []struct {
		name          string
		clientModel   string
		outbound      string
		canonicalID   int
		wantModel     string
		wantCanonical string
	}{
		{
			name:          "canonical resolves: 标准名优先",
			clientModel:   "minimax-m3",
			outbound:      "minimax-m3-vendor-raw",
			canonicalID:   42,
			wantModel:     "minimax-m3",
			wantCanonical: "minimax-m3",
		},
		{
			name:          "no canonical: client 优先于 outbound",
			clientModel:   "claude-sonnet-4-5",
			outbound:      "claude-sonnet-4-5-20251001",
			canonicalID:   0,
			wantModel:     "claude-sonnet-4-5",
			wantCanonical: "claude-sonnet-4-5",
		},
		{
			name:          "no canonical, no client: outbound 兜底（向后兼容）",
			clientModel:   "",
			outbound:      "gpt-4o-2024-08-06",
			canonicalID:   0,
			wantModel:     "gpt-4o-2024-08-06",
			wantCanonical: "gpt-4o-2024-08-06",
		},
		{
			name:          "canonical resolves 但 client/outbound 都不同: 必须用 canonical",
			clientModel:   "gpt-4o",
			outbound:      "azure-gpt-4o-mini",
			canonicalID:   42,
			wantModel:     "minimax-m3",
			wantCanonical: "minimax-m3",
		},
		{
			// canonicalID > 0 但 canonical_name 为空（模型被删除/inactive）：
			// 应回退到 clientModel，而不是用空字符串覆盖。
			name:          "canonicalID > 0 但 resolves 为空: 回退到 client",
			clientModel:   "deepseek-v3",
			outbound:      "deepseek-chat-v3-raw",
			canonicalID:   999, // cache 未预热 → 返回 ""
			wantModel:     "deepseek-v3",
			wantCanonical: "deepseek-v3",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hub.LiveRequestFromTelemetry(
				context.Background(),
				"req-1",
				time.Now().UTC(),
				"tenant-a",
				c.clientModel,
				c.outbound,
				c.canonicalID,
				"", // canonicalNameIn (空,依赖 CanonicalNameFor fallback)
				"openai",
				"success",
				true,
				nil, // errorKind
				nil, // latencyMs
				nil, // promptTokens
				nil, // completionTokens
				nil, // totalTokens
				nil, // costUSD
				nil, // failureStage
				"",  // agentName
				"",  // agentType
				"",  // clientProtocol
				nil, // entry
			)
			if got.Model != c.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, c.wantModel)
			}
			if got.CanonicalName != c.wantCanonical {
				t.Errorf("CanonicalName = %q, want %q", got.CanonicalName, c.wantCanonical)
			}
			// 同时验证分维度 key 与显示 model 一致：
			// 同一标准模型的请求都应落到同一 model 维度 key 下。
			if key := liveStreamDimensionKey("model", got); key != "" && key != normalizeModelKey(c.wantModel) {
				t.Errorf("liveStreamDimensionKey(model)=%q, must equal normalizeModelKey(%q)=%q",
					key, c.wantModel, normalizeModelKey(c.wantModel))
			}
		})
	}
}

// TestCanonicalNameFor_NilHubDoesNotPanic guards against the 2026-07-16
// regression where CanonicalNameFor's cache check was moved BEFORE the
// h == nil guard, which would cause a nil pointer dereference panic on
// h.canonicalCache.Load(...) when called with a nil receiver.
//
// LiveRequestFromTelemetry and ModelVendorFor already defend against nil h,
// so the live stream pipeline must never panic even when the hub is
// uninitialized (e.g. during graceful shutdown or test wiring).
func TestCanonicalNameFor_NilHubDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CanonicalNameFor panicked on nil receiver: %v", r)
		}
	}()

	var nilHub *LiveStreamSSEHub
	got := nilHub.CanonicalNameFor(context.Background(), 42)
	if got != "" {
		t.Errorf("nil hub should return empty string, got %q", got)
	}
}
