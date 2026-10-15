package bg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
)

// 2026-10-13（Vapeur deepseek-v4-pro 探针三连败事故）判据：
// 聚合中转把 chat ping 内部转成 Responses 形态，chat 腿 max_tokens=10
// 撞上 ≥16 下限（"Invalid 'max_output_tokens': must be greater than or
// equal to 16, got 10"）→ 探针 3 连败 → 节点停 5 分钟 → 用户流量
// db_empty 连坐。所有 ping 形态的 token 预算不得低于 16。
func TestProbePingBodiesRespectTokenFloor(t *testing.T) {
	if providercap.ProbePingMaxTokens < 16 {
		t.Fatalf("ProbePingMaxTokens = %d, must be >= 16 (responses API floor)", providercap.ProbePingMaxTokens)
	}
	// chat 腿（default 分支）
	body := directProbeBody("deepseek-v4-pro", "openai-completions")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("default leg body is not valid JSON: %v\n%s", err, body)
	}
	if mt, _ := parsed["max_tokens"].(float64); int(mt) < 16 {
		t.Fatalf("chat-leg ping max_tokens = %v, must be >= 16 (relay converts to responses under the hood)", mt)
	}
	// responses 腿
	bodyR := directProbeBody("deepseek-v4-pro", "openai-responses")
	var parsedR map[string]any
	if err := json.Unmarshal([]byte(bodyR), &parsedR); err != nil {
		t.Fatalf("responses leg body is not valid JSON: %v", err)
	}
	if mt, _ := parsedR["max_output_tokens"].(float64); int(mt) < 16 {
		t.Fatalf("responses-leg max_output_tokens = %v, must be >= 16", mt)
	}
}

// self_check_worker 的 Sprintf ping 模板曾是裸字符串（2026-10-13 修转义时
// 现场又抓到一次坏 JSON），钉住它产出的必须是合法 JSON 且达到下限。
func TestSelfCheckPingTemplateIsJSONWithFloor(t *testing.T) {
	pingBody := selfCheckPingBody("deepseek-v4-pro")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(pingBody), &parsed); err != nil {
		t.Fatalf("self-check ping body is not valid JSON: %v\n%s", err, pingBody)
	}
	if mt, _ := parsed["max_tokens"].(float64); int(mt) < 16 {
		t.Fatalf("self-check ping max_tokens = %v, must be >= 16", mt)
	}
	if !strings.Contains(pingBody, "ping") {
		t.Fatal("self-check ping body lost its ping content")
	}
}
