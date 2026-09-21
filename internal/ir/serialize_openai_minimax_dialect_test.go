package ir

import (
	"encoding/json"
	"testing"
)

// R52 回归钉桩：mask_sensitive_info / bot_setting 是 MiniMax 专有字段，
// 2026-09-21 提升为 IR 一等字段后 extensions_restore 的方言守卫（只遍历
// req.Extensions）再也看不到它们——发射侧必须按 resolveTargetDialect
// 门控，否则会把专有字段无条件发给所有 openai-chat 上游（严格校验上游
// additionalProperties:false 直接 400 / 参数泄漏）。
func TestSerializeOpenAI_MiniMaxPrivateFields_DialectGated(t *testing.T) {
	newReq := func() *InternalRequest {
		req := &InternalRequest{Model: "test-model"}
		v := true
		req.MaskSensitiveInfo = &v
		req.BotSetting = []BotSetting{{BotName: "bot", Content: "hi"}}
		return req
	}

	cases := []struct {
		name           string
		targetProvider string
		wantEmit       bool
	}{
		{"minimax target emits", "minimax", true},
		{"openai target drops", "openai", false},
		{"deepseek target drops", "deepseek", false},
		{"mistral target drops", "mistral", false},
		// 路由前序列化点（TargetProvider 空）fail-closed：方言回落
		// openai-chat，不在 MiniMax Dialects 内。
		{"pre-routing empty target drops", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newReq()
			req.TargetProvider = tc.targetProvider
			raw, err := SerializeOpenAI(req)
			if err != nil {
				t.Fatalf("SerializeOpenAI: %v", err)
			}
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for _, key := range []string{"mask_sensitive_info", "bot_setting"} {
				_, got := out[key]
				if got != tc.wantEmit {
					t.Fatalf("field %q present=%v, want %v (target=%q)", key, got, tc.wantEmit, tc.targetProvider)
				}
			}
		})
	}
}

// repetition_penalty 维持全目标发射（KindPortable 时代即全目标还原，
// registry Dialects 声明的是“官方支持面”而非独占面）。
func TestSerializeOpenAI_RepetitionPenalty_Unconditional(t *testing.T) {
	v := 1.15
	req := &InternalRequest{Model: "test-model", RepetitionPenalty: &v}
	raw, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["repetition_penalty"]; !ok {
		t.Fatalf("repetition_penalty must stay unconditional (pre-existing behavior)")
	}
}
