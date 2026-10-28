package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
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

// TestLiveRequestFromTelemetry_ModelSelectionFields 是 2026-10-28（live 拓扑·
// 模型选择层表达轮）的投影判据：admin.LiveRequest 新增四个 additive 字段
// （client_model / is_auto_request / task_type / token_band），全部来自
// telemetry.RequestLogEntry 的既有投影 —— 前端据此表达「auto 按任务选模 /
// 内部直连 / 指定模型」三层选路与「缓存→脱敏→压缩」里的压缩档位。
//
// 断言三件事：
//  1. entry 带值 ⇒ 四个字段如实上 SSE；
//  2. entry 为 nil / 字段缺省 ⇒ 四个字段保持零值（optional 语义，
//     前端按「未知」渲染，禁止零值冒充）；
//  3. IsAutoRequest=false 时不序列化（omitempty），线上不会出现
//     "is_auto_request":false 与旧客户端产生语义分歧。
func TestLiveRequestFromTelemetry_ModelSelectionFields(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	strPtr := func(s string) *string { return &s }
	boolPtr := func(b bool) *bool { return &b }

	entry := &telemetry.RequestLogEntry{
		RequestID:     "req-auto-1",
		ClientModel:   strPtr("glm-5.3"),
		IsAutoRequest: boolPtr(true),
		TaskType:      strPtr("code"),
		TokenBand:     strPtr("forced"),
		// 环回口径轮（2026-10-28 遗留②）：strategy 非空且非 delta_append
		// ⇒ Compressed=true。
		CompressionStrategy: strPtr("sliding_window_count"),
	}
	got := hub.LiveRequestFromTelemetry(
		context.Background(), "req-auto-1", time.Now().UTC(), "tenant-a",
		"glm-5.3", "glm-5.3-raw", 0, "", "zhipu",
		"success", true, nil, nil, nil, nil, nil, nil, nil,
		"", "", "", entry,
	)
	if got.ClientModel != "glm-5.3" {
		t.Errorf("ClientModel = %q, want %q", got.ClientModel, "glm-5.3")
	}
	if !got.IsAutoRequest {
		t.Error("IsAutoRequest must be true when entry says so")
	}
	if got.TaskType != "code" {
		t.Errorf("TaskType = %q, want %q", got.TaskType, "code")
	}
	if got.TokenBand != "forced" {
		t.Errorf("TokenBand = %q, want %q", got.TokenBand, "forced")
	}
	if !got.Compressed {
		t.Error("Compressed must be true for a real compression strategy (sliding_window_count)")
	}

	// delta_append 是会话增量组装不是压缩：strategy 有值但 Compressed
	// 必须为 false（211 全量实测 delta_append=0，此排除是语义判据不是凑数）。
	deltaOnly := hub.LiveRequestFromTelemetry(
		context.Background(), "req-delta", time.Now().UTC(), "tenant-a",
		"glm-5.3", "glm-5.3-raw", 0, "", "zhipu",
		"success", true, nil, nil, nil, nil, nil, nil, nil,
		"", "", "", &telemetry.RequestLogEntry{
			RequestID:           "req-delta",
			TokenBand:           strPtr("below"),
			CompressionStrategy: strPtr("delta_append"),
		},
	)
	if deltaOnly.Compressed {
		t.Error("delta_append is session delta assembly, not compression — Compressed must stay false")
	}

	// band 越线但 strategy 空（summary 失败且 trim 无收缩）：档位观测在，
	// 实际压缩没发生 ⇒ Compressed=false。band ≠ 压缩发生（实测修正）。
	bandOnly := hub.LiveRequestFromTelemetry(
		context.Background(), "req-band-only", time.Now().UTC(), "tenant-a",
		"glm-5.3", "glm-5.3-raw", 0, "", "zhipu",
		"success", true, nil, nil, nil, nil, nil, nil, nil,
		"", "", "", &telemetry.RequestLogEntry{
			RequestID: "req-band-only",
			TokenBand: strPtr("forced"),
		},
	)
	if bandOnly.Compressed {
		t.Error("token_band=forced with empty strategy = threshold observed, nothing applied — Compressed must stay false")
	}

	// 缺省路径：entry 全空 + 入参 clientModel 也为空 ⇒ 五字段零值。
	bare := hub.LiveRequestFromTelemetry(
		context.Background(), "req-plain", time.Now().UTC(), "tenant-a",
		"", "", 0, "", "openai",
		"success", true, nil, nil, nil, nil, nil, nil, nil,
		"", "", "", &telemetry.RequestLogEntry{RequestID: "req-plain"},
	)
	if bare.ClientModel != "" || bare.IsAutoRequest || bare.TaskType != "" || bare.TokenBand != "" || bare.Compressed {
		t.Errorf("absent entry fields must stay zero-value, got %+v", bare)
	}

	// omitempty 序列化：缺省字段不上线（旧客户端不会看到 "is_auto_request":false
	// 与既有语义产生分歧；线上帧保持向后兼容的 optional 形态）。
	blob, err := json.Marshal(bare)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"client_model", "is_auto_request", "task_type", "token_band", "compressed"} {
		if strings.Contains(string(blob), key) {
			t.Errorf("wire must omit %q when absent, got %s", key, blob)
		}
	}
	// 反向：带值时五个键必须都在线上。
	full, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	for _, key := range []string{"client_model", "is_auto_request", "task_type", "token_band", "compressed"} {
		if !strings.Contains(string(full), key) {
			t.Errorf("wire must carry %q when present, got %s", key, full)
		}
	}
}

// TestLiveRequestRedisPayloadRoundTripsSelectionFields（2026-10-28 审计轮）钉住
// 模型选择层五字段在 Redis payload 往返中的保真。实时帧直接广播 LiveRequest
// 结构体，但 initial_data 回放 / snapshot_refresh / tile delta 全部从
// liveRequestRedisPayload 重建——上一轮四字段（client_model / is_auto_request /
// task_type / token_band）与本轮 compressed 都只接了 LiveRequest 与前端，
// 漏了这条持久化路径：Redis 部署上页面加载的整个初始窗口压缩格恒 0、
// 模型选择全「未知」。本测试是往返漂移门：往 LiveRequest 加了字段而 payload
// 忘了跟，这里红。
func TestLiveRequestRedisPayloadRoundTripsSelectionFields(t *testing.T) {
	full := LiveRequest{
		Type:          "request",
		RequestID:     "req-rt-1",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "tenant-a",
		ClientModel:   "auto",
		IsAutoRequest: true,
		TaskType:      "code",
		TokenBand:     "below",
		Compressed:    true,
	}
	data, err := marshalLiveRequestRedisPayload(full)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := unmarshalLiveRequestRedisPayload(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ClientModel != "auto" {
		t.Errorf("ClientModel = %q, want %q (payload must persist it)", got.ClientModel, "auto")
	}
	if !got.IsAutoRequest {
		t.Error("IsAutoRequest must survive the payload round-trip")
	}
	if got.TaskType != "code" {
		t.Errorf("TaskType = %q, want %q (payload must persist it)", got.TaskType, "code")
	}
	if got.TokenBand != "below" {
		t.Errorf("TokenBand = %q, want %q (payload must persist it)", got.TokenBand, "below")
	}
	if !got.Compressed {
		t.Error("Compressed must survive the payload round-trip — snapshots rebuild from it and the compress grid counts 0 otherwise")
	}

	// omitempty 负例：五字段缺省时 payload 不得带出这些键（与 SSE 线上
	// 契约同型——旧 payload 的消费者不会看到 "is_auto_request":false）。
	bare, err := marshalLiveRequestRedisPayload(LiveRequest{RequestID: "req-rt-2", Ts: full.Ts})
	if err != nil {
		t.Fatalf("marshal bare: %v", err)
	}
	for _, key := range []string{"client_model", "is_auto_request", "task_type", "token_band", "compressed"} {
		if strings.Contains(bare, key) {
			t.Errorf("payload must omit %q when absent, got %s", key, bare)
		}
	}
}
