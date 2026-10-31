package ir

import (
	"encoding/json"
	"testing"
)

// 2026-11-01 参数兼容静态化轮：chat 入向顶层 reasoning 对象的出站裁决
// 端到端判据（extensions_restore 特例，四腿实机证据见
// docs/audit/2026-11-01-参数兼容静态化轮-跨端点reasoning实证.md）。
//
// 变异臂 M5 靶：extensions_restore 的 reasoning 特例整块删除 ⇒ 本文件红；
// 变异臂 M6 靶：特例内「下划线存在性探测」删掉（outHasUnderscoreEffort
// 恒传 false）⇒ 双表达臂红。

// parseChat 解析 chat 入向体并返回 IR。
func parseChat(t *testing.T, body string) *InternalRequest {
	t.Helper()
	irReq, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	return irReq
}

// mustMap 反序列化出站体。
func mustMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal outbound body: %v", err)
	}
	return m
}

// TestRestoreReasoningObject_ChatLineDialects 四臂端到端：
// 同一个 chat 入向体（对象形态 effort），目标方言决定出站形态。
func TestRestoreReasoningObject_ChatLineDialects(t *testing.T) {
	const body = `{
		"model": "glm-5.3",
		"messages": [{"role":"user","content":"hi"}],
		"max_tokens": 64,
		"reasoning": {"effort":"high"}
	}`

	t.Run("GLM 目标：对象翻译成 reasoning_effort（zhipu 静默忽略对象，意图不再无声丢失）", func(t *testing.T) {
		req := parseChat(t, body)
		req.TargetProvider = "zhipu"
		out := mustMap(t, mustSerialize(t, req))
		if out["reasoning_effort"] != "high" {
			t.Fatalf("zhipu 目标应得 reasoning_effort=high, got %v", out["reasoning_effort"])
		}
		if _, exists := out["reasoning"]; exists {
			t.Fatalf("zhipu 目标不得再带 reasoning 对象: %v", out["reasoning"])
		}
	})

	t.Run("Ark 目标：同上（volcengine 忽略对象；reasoning_effort=minimal 确定性归零的方言）", func(t *testing.T) {
		req := parseChat(t, body)
		req.TargetProvider = "volcengine"
		out := mustMap(t, mustSerialize(t, req))
		if out["reasoning_effort"] != "high" {
			t.Fatalf("ark 目标应得 reasoning_effort=high, got %v", out["reasoning_effort"])
		}
	})

	t.Run("OpenRouter 目标：对象原样保留（原生生效方言，零回归）", func(t *testing.T) {
		req := parseChat(t, body)
		req.TargetProvider = "openrouter"
		out := mustMap(t, mustSerialize(t, req))
		obj, ok := out["reasoning"].(map[string]any)
		if !ok || obj["effort"] != "high" {
			t.Fatalf("openrouter 目标应保留 reasoning 对象, got %v", out["reasoning"])
		}
	})

	t.Run("nvidia 目标：整对象裁剪（NIM 硬 400，不再吃 400 剥参往返）", func(t *testing.T) {
		req := parseChat(t, body)
		req.TargetProvider = "nvidia"
		out := mustMap(t, mustSerialize(t, req))
		if _, exists := out["reasoning"]; exists {
			t.Fatalf("nvidia 目标不得携带 reasoning 对象（NIM 400）")
		}
		if _, exists := out["reasoning_effort"]; exists {
			t.Fatalf("客户端只发了对象形态，nvidia 出站不得凭空造 reasoning_effort（保守裁剪，非翻译）")
		}
	})
}

// TestRestoreReasoningObject_DualSpelling 任务问题 2 的线上判据：
// 客户端同时发 reasoning{effort} 与 reasoning_effort ——
// parse_openai 把下划线消费进 IR（serialize 恒发射），对象留 Extensions。
func TestRestoreReasoningObject_DualSpelling(t *testing.T) {
	const body = `{
		"model": "glm-5.3",
		"messages": [{"role":"user","content":"hi"}],
		"max_tokens": 64,
		"reasoning_effort": "high",
		"reasoning": {"effort":"high"}
	}`

	t.Run("双表达 effort-only：出站只剩下划线（四腿实证下划线处处生效；OpenRouter 冲突 400 类消除）", func(t *testing.T) {
		req := parseChat(t, body)
		req.TargetProvider = "openrouter"
		out := mustMap(t, mustSerialize(t, req))
		if out["reasoning_effort"] != "high" {
			t.Fatalf("下划线必须保留, got %v", out["reasoning_effort"])
		}
		if _, exists := out["reasoning"]; exists {
			t.Fatalf("effort-only 对象必须静态收口: %v", out["reasoning"])
		}
	})

	t.Run("双表达 + OpenRouter 旋钮：effort 让位、旋钮保留", func(t *testing.T) {
		req := parseChat(t, body)
		req.Extensions["reasoning"] = json.RawMessage(`{"effort":"high","exclude":true,"max_tokens":512}`)
		req.TargetProvider = "openrouter"
		out := mustMap(t, mustSerialize(t, req))
		if out["reasoning_effort"] != "high" {
			t.Fatalf("下划线必须保留, got %v", out["reasoning_effort"])
		}
		obj, ok := out["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("旋钮对象必须保留: %v", out["reasoning"])
		}
		if _, has := obj["effort"]; has {
			t.Fatalf("对象内 effort 必须剥掉（防 OpenRouter 冲突 400）: %v", obj)
		}
		if obj["exclude"] != true || obj["max_tokens"] != float64(512) {
			t.Fatalf("exclude/max_tokens 旋钮必须原样保留: %v", obj)
		}
	})

	t.Run("双表达值冲突：以 IR 下划线值为准（每方言一致；nvidia 不再吃对象 400）", func(t *testing.T) {
		req := parseChat(t, body)
		req.Extensions["reasoning"] = json.RawMessage(`{"effort":"low"}`)
		// IR 侧显式改为 low：模拟客户端下划线 low + 对象 high 的冲突形态
		// （parse 已把下划线值放进 IR.Reasoning，这里直接覆盖以构造冲突）。
		if req.Reasoning == nil {
			req.Reasoning = &ReasoningConfig{Effort: "low"}
		} else {
			req.Reasoning.Effort = "low"
		}
		req.TargetProvider = "nvidia"
		out := mustMap(t, mustSerialize(t, req))
		if out["reasoning_effort"] != "low" {
			t.Fatalf("nvidia 出站应是 IR 下划线值 low, got %v", out["reasoning_effort"])
		}
		if _, exists := out["reasoning"]; exists {
			t.Fatalf("冲突对象必须被裁剪: %v", out["reasoning"])
		}
	})
}

// mustSerialize 序列化 chat 出站体。
func mustSerialize(t *testing.T, req *InternalRequest) []byte {
	t.Helper()
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	return out
}
