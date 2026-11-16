// live_stage_tree_projection_test.go — /live 阶段树 Phase 1 的实时帧投影判据
// （2026-11-16）。
//
// 背景：阶段树要显示「缓存命中率」，而**分母口径取决于上游协议**
// （domains/streaming/usage.go 的 CacheTokenConvention）：
//
//	· OpenAI 口径   prompt_tokens ⊇ cached_tokens      ⇒ 分母 = prompt
//	· Anthropic 口径 input_tokens ∥ cache_read_input_tokens ⇒ 分母 = prompt + cache_read
//
// 没有 upstream_protocol 上帧 ⇒ 前端只能给一个**混了两种口径的假命中率**
// （2026-10-06 真库事故同族：cache_read 是 prompt 的 25 倍、cost_usd 负数 1628 行）。
//
// 断言三件事：
//  1. entry 带 upstream_protocol ⇒ 字段如实上 SSE；
//  2. entry 为 nil / 字段缺省 ⇒ 字段保持空（optional 语义：前端显示「—」，
//     **禁止零值冒充**）；
//  3. 序列化后不出现 "upstream_protocol":""（omitempty ⇒ 旧客户端不解析它）。
package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestLiveRequestFromTelemetry_UpstreamProtocol 是上游协议上帧的判据。
func TestLiveRequestFromTelemetry_UpstreamProtocol(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	strPtr := func(s string) *string { return &s }

	// ① 有值 ⇒ 如实上帧（两种口径都要能带上）。
	for _, proto := range []string{"openai", "anthropic"} {
		got := hub.LiveRequestFromTelemetry(
			context.Background(), "req-proto-"+proto, time.Now().UTC(), "tenant-a",
			"glm-5.3", "glm-5.3-raw", 0, "", "zhipu",
			"success", true, nil, nil, nil, nil, nil, nil, nil,
			"", "", "", &telemetry.RequestLogEntry{
				RequestID:        "req-proto-" + proto,
				UpstreamProtocol: strPtr(proto),
			},
		)
		if got.UpstreamProtocol != proto {
			t.Errorf("UpstreamProtocol = %q, want %q", got.UpstreamProtocol, proto)
		}
	}

	// ② 缺省 ⇒ 空串（= 未上报）。前端据此显示「—」而不是 0%。
	bare := hub.LiveRequestFromTelemetry(
		context.Background(), "req-proto-bare", time.Now().UTC(), "tenant-a",
		"glm-5.3", "glm-5.3-raw", 0, "", "zhipu",
		"success", true, nil, nil, nil, nil, nil, nil, nil,
		"", "", "", &telemetry.RequestLogEntry{RequestID: "req-proto-bare"},
	)
	if bare.UpstreamProtocol != "" {
		t.Errorf("UpstreamProtocol must stay empty when not reported, got %q", bare.UpstreamProtocol)
	}

	// entry 整体为 nil 也不能 panic（兜底路径同款语义）。
	nilEntry := hub.LiveRequestFromTelemetry(
		context.Background(), "req-proto-nil", time.Now().UTC(), "tenant-a",
		"glm-5.3", "glm-5.3-raw", 0, "", "zhipu",
		"success", true, nil, nil, nil, nil, nil, nil, nil,
		"", "", "", nil,
	)
	if nilEntry.UpstreamProtocol != "" {
		t.Errorf("nil entry must leave UpstreamProtocol empty, got %q", nilEntry.UpstreamProtocol)
	}
}

// TestLiveRequest_UpstreamProtocolOmitEmpty 钉 omitempty 语义：
// 未上报时**不出现**该键（旧客户端不会因新字段产生解析分歧）。
func TestLiveRequest_UpstreamProtocolOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(LiveRequest{RequestID: "r1", Ts: "2026-11-16T00:00:00Z"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "upstream_protocol") {
		t.Errorf("empty upstream_protocol must be omitted, got %s", raw)
	}

	raw2, err := json.Marshal(LiveRequest{RequestID: "r1", Ts: "2026-11-16T00:00:00Z", UpstreamProtocol: "anthropic"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw2), `"upstream_protocol":"anthropic"`) {
		t.Errorf("upstream_protocol must be serialized when reported, got %s", raw2)
	}
}
