package ir

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 还原通道覆盖矩阵（2026-11-11 审计第二轮 · 批 1）。
//
// ── 背景与不变量 ─────────────────────────────────────────────────────────
// 「非原厂默认的参数经 Extensions 保存、还原时还原」的完整语义 = 对每个
// IR 载体 × 每个目标协议，处置必须是三者之一：
//
//	① consumed   序列化器消费（结构化发射 / 翻译映射）
//	② disclosed  丢弃但发 ir_protocol_loss 事件（诚实披露）
//	（③ silent   静默蒸发 —— 本矩阵的敌人，出现即缺陷）
//
// 第一轮审计（gemini_only_loss）收口了 Extensions 腿；本轮批 1 收口
// 结构化 IR 字段腿（→Anthropic seed/repetition_penalty、→Gemini 20 字段、
// →Ollama 24 字段、工具级 Extra）。本文件把这些处置**登记成机器可查的
// 矩阵**，并用反射保证**未来新增 IR 字段必须显式决策**（fail-closed）：
// 加一个结构化字段而不登记处置 ⇒ 矩阵测试红。
//
// 断言纪律（第一轮教训）：行为断言打在 wire / loss 事件上，不打中间态。

// structuredFieldState 是一个 IR 结构化字段在某个序列化器上的登记处置。
type structuredFieldState struct {
	// consumed = 序列化器发射或翻译；disclosed = 丢弃但有 loss 事件。
	consumed bool
	note     string
}

// structuredFieldMatrix 字段 → 各序列化器处置。键 = InternalRequest 的
// json tag。序列化器顺序：openai-chat / anthropic-messages / gemini /
// responses / ollama。
//
// 维护纪律：改任何序列化器的消费集（新增发射或披露）必须同步本表 ——
// 表与代码的漂移由下方行为抽查测试（TestMatrixSpotChecks…）咬住。
var structuredFieldMatrix = map[string][5]structuredFieldState{
	"model":               {ok(), ok(), ok(), ok(), ok()},
	"messages":            {ok(), ok(), ok(), ok(), ok()},
	"system":              {ok(), ok(), ok(), ok(), ok()}, // ollama 2026-11-11 起发射（批 1 修复）
	"tools":               {ok(), ok(), ok(), ok(), ok()},
	"tool_choice":         {ok(), ok(), ok(), ok(), st(false, "ollama 无 tool_choice；披露")},
	"max_tokens":          {ok(), ok(), ok(), ok(), ok()},
	"temperature":         {ok(), ok(), ok(), ok(), ok()},
	"top_p":               {ok(), ok(), ok(), ok(), ok()},
	"top_k":               {ok(), ok(), ok(), st(false, "responses 无 top_k；披露"), ok()},
	"stop":                {ok(), ok(), ok(), st(false, "responses 无 stop；披露"), ok()},
	"parallel_tool_calls": {ok(), st(false, "披露"), st(false, "披露"), ok(), st(false, "披露")},
	"stream":              {ok(), ok(), ok(), ok(), ok()},

	// 思考/推理族（Slice 2 另立项；丢弃路径已有披露）
	"thinking":  {ok(), ok(), ok(), ok(), st(false, "披露 ollama_reasoning_drop")},
	"reasoning": {ok(), ok(), ok(), ok(), st(false, "披露 ollama_reasoning_drop")},

	// Anthropic 专有
	"cache_control":      {st(false, "披露"), ok(), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"documents":          {st(false, "披露"), ok(), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"mcp_servers":        {st(false, "披露"), ok(), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"context_management": {st(false, "披露"), ok(), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"container":          {st(false, "披露"), ok(), st(false, "披露"), st(false, "披露"), st(false, "披露")},

	// OpenAI 族
	"frequency_penalty":    {ok(), st(false, "披露"), ok(), st(false, "披露"), ok()}, // ollama 2026-11-11 起映射 options
	"presence_penalty":     {ok(), st(false, "披露"), ok(), st(false, "披露"), ok()},
	"logprobs":             {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"top_logprobs":         {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"seed":                 {ok(), st(false, "披露"), ok(), st(false, "披露"), ok()},
	"response_format":      {ok(), st(false, "披露"), ok(), ok(), ok()}, // ollama 2026-11-11 起映射 format
	"n":                    {ok(), st(false, "披露"), ok(), st(false, "披露"), st(false, "披露")},
	"user":                 {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"logit_bias":           {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"store":                {ok(), st(false, "披露"), st(false, "披露"), ok(), st(false, "披露")},
	"service_tier":         {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"prediction":           {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"verbosity":            {ok(), st(false, "披露"), st(false, "披露"), ok(), st(false, "披露")},
	"web_search_options":   {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"prompt_cache_key":     {ok(), st(false, "披露"), st(false, "披露"), ok(), st(false, "披露")},
	"safety_identifier":    {ok(), st(false, "披露"), st(false, "披露"), ok(), st(false, "披露")},
	"previous_response_id": {st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "responses 无链式 ID；披露"), st(false, "披露")},
	"truncation":           {ok(), st(false, "披露"), st(false, "披露"), ok(), st(false, "披露")},

	// 多模态
	"modalities":   {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"audio_config": {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},

	// Gemini 专有（非 Gemini 目标走 gemini_only_loss 统一披露）
	"safety_settings": {st(false, "披露"), st(false, "披露"), ok(), st(false, "披露"), st(false, "披露")},
	"cached_content":  {st(false, "披露"), st(false, "披露"), ok(), st(false, "披露"), st(false, "披露")},

	// MiniMax 专有（paramreg Dialects=[openai-chat] 守卫 + IRFieldAllowedForDialect）
	"mask_sensitive_info": {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},
	"bot_setting":         {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), st(false, "披露")},

	// 通用扩展
	"repetition_penalty": {ok(), st(false, "披露"), st(false, "披露"), st(false, "披露"), ok()},

	// 混合载体：metadata 的 user_id/other 是客户端可见部分（anthropic/
	// responses wire 发射）；request_id/project/tags/turn_total 是内部归因
	// 载体（types.go 注释），gemini/ollama 只在携带客户端可见数据时披露。
	"metadata": {st(false, "chat wire 无 metadata；披露(仅客户端可见部分)"), ok(), st(false, "披露(仅客户端可见部分)"), ok(), st(false, "披露(仅客户端可见部分)")},

	// 元字段（非客户端参数，不参与矩阵）
}

// ok 构造 consumed 状态。
func ok() structuredFieldState { return structuredFieldState{consumed: true, note: "consumed"} }

// st 构造 disclosed 状态。
func st(consumed bool, note string) structuredFieldState {
	return structuredFieldState{consumed: consumed, note: note}
}

// metaFields 是 InternalRequest 上的非客户端参数（协议元数据 / 内部载体），
// 不参与「consumed 或 disclosed」不变量。
var metaFields = map[string]bool{
	"source_protocol":    true,
	"extensions":         true,
	"class":              true,
	"due_at":             true,
	"native_passthrough": true,
	"target_provider":    true,
}

// TestStructuredFieldCoverageMatrix 矩阵主断言：
// ① 反射发现的每个 InternalRequest 客户端参数字段都必须登记在矩阵里
//
//	（fail-closed：新增字段不登记 ⇒ 红）；
//
// ② 每个登记项的处置必须是 consumed 或 disclosed（不存在第三态）。
func TestStructuredFieldCoverageMatrix(t *testing.T) {
	rt := reflect.TypeOf(InternalRequest{})
	seen := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || metaFields[name] {
			continue
		}
		seen[name] = true
		states, registered := structuredFieldMatrix[name]
		if !registered {
			t.Errorf("IR 结构化字段 %q 未登记处置矩阵（consumed/disclosed 二选一，静默=缺陷）: %s %s",
				name, f.Name, f.Type)
			continue
		}
		for si, s := range states {
			if s.consumed || s.note != "" {
				continue
			}
			t.Errorf("字段 %q 序列化器 #%d 的登记既非 consumed 也无披露说明: %+v", name, si, s)
		}
	}
	for name := range structuredFieldMatrix {
		if !seen[name] {
			t.Errorf("矩阵登记了不存在的 IR 字段 %q（字段被删或改名后须同步矩阵）", name)
		}
	}
}

// TestMatrixSpotChecks_Batch1Pins 批 1 修复的行为抽查（wire / loss 事件级）。
func TestMatrixSpotChecks_Batch1Pins(t *testing.T) {
	t.Run("ollama_system_prompt_emitted", func(t *testing.T) {
		// 生产路径形态：走 ParseOpenAI（system 被摘进 IR.System），不是手工构造 IR。
		body := []byte(`{"model":"qwen3:8b","messages":[
			{"role":"system","content":"You are a helpful assistant."},
			{"role":"user","content":"hi"}]}`)
		req, err := ParseOpenAI(body)
		if err != nil {
			t.Fatal(err)
		}
		if req.System == nil {
			t.Fatal("前置：parse 应把 system 消息摘进 IR.System")
		}
		if len(req.Messages) != 1 {
			t.Fatalf("前置：parse 后 Messages 应只剩 1 条，got %d", len(req.Messages))
		}
		out, err := SerializeOllama(req)
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		if len(m.Messages) != 2 {
			t.Fatalf("ollama messages 应为 2 条（system+user），got %d: %s", len(m.Messages), out)
		}
		if m.Messages[0].Role != "system" || m.Messages[0].Content != "You are a helpful assistant." {
			t.Fatalf("首条必须是客户端 system 提示词，got %+v", m.Messages[0])
		}
	})

	t.Run("ollama_response_format_translation", func(t *testing.T) {
		req := minimalOllamaReq()
		req.ResponseFormat = &ResponseFormat{Type: "json_object"}
		out := mustOllama(t, req)
		if got, _ := out["format"].(string); got != "json" {
			t.Fatalf("json_object 应映射 format:\"json\"，got %v", out["format"])
		}

		schema := map[string]any{"type": "object"}
		schemaBytes, _ := json.Marshal(schema)
		req2 := minimalOllamaReq()
		req2.ResponseFormat = &ResponseFormat{Type: "json_schema", Schema: schemaBytes}
		out2 := mustOllama(t, req2)
		var got map[string]any
		raw, _ := json.Marshal(out2["format"])
		if err := json.Unmarshal(raw, &got); err != nil || got["type"] != "object" {
			t.Fatalf("json_schema 应映射 format:<schema 对象>，got %v (err=%v)", out2["format"], err)
		}

		req3 := minimalOllamaReq()
		req3.ResponseFormat = &ResponseFormat{Type: "text"}
		out3 := mustOllama(t, req3)
		if _, exists := out3["format"]; exists {
			t.Fatalf("text 无等价物，不应发射 format: %v", out3["format"])
		}

		// Ollama 原生客户端的 ollama.format 命名空间直通优先，不被覆盖。
		req4 := minimalOllamaReq()
		req4.ResponseFormat = &ResponseFormat{Type: "json_object"}
		req4.Extensions = map[string]json.RawMessage{"ollama.format": json.RawMessage(`{"type":"object"}`)}
		out4 := mustOllama(t, req4)
		var got4 map[string]any
		raw4, _ := json.Marshal(out4["format"])
		if err := json.Unmarshal(raw4, &got4); err != nil || got4["type"] != "object" {
			t.Fatalf("ollama.format 直通应优先，got %v", out4["format"])
		}
	})

	t.Run("ollama_penalty_params_consumed", func(t *testing.T) {
		req := minimalOllamaReq()
		fp, pp := 0.5, 0.25
		req.FrequencyPenalty = &fp
		req.PresencePenalty = &pp
		out := mustOllama(t, req)
		opts := out["options"].(map[string]any)
		if opts["frequency_penalty"] != 0.5 || opts["presence_penalty"] != 0.25 {
			t.Fatalf("penalty 应进 options，got %v", opts)
		}
	})

	t.Run("ollama_tool_extras_merged", func(t *testing.T) {
		req := minimalOllamaReq()
		req.Tools = []ToolDefinition{{
			Name:       "get_weather",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Extra:      json.RawMessage(`{"vendor_tool_flag":"x"}`),
		}}
		out := mustOllama(t, req)
		tool := out["tools"].([]any)[0].(map[string]any)
		if tool["vendor_tool_flag"] != "x" {
			t.Fatalf("工具 Extra 应并回 ollama 工具顶层，got %v", tool)
		}
		if tool["type"] != "function" {
			t.Fatalf("Extra 不得覆盖已写键，got %v", tool)
		}
	})

	t.Run("ollama_structured_fields_disclosed", func(t *testing.T) {
		req := minimalOllamaReq()
		store := true
		req.ToolChoice = &ToolChoice{Type: "auto"}
		req.Store = &store
		req.MCPServers = []MCPServer{{Name: "m"}}
		losses := captureProtocolLoss(t, func() {
			_, _ = SerializeOllama(req)
		})
		got := map[string]bool{}
		for _, ev := range losses {
			got[ev.FieldPath] = true
		}
		for _, want := range []string{"tool_choice", "store", "mcp_servers"} {
			if !got[want] {
				t.Fatalf("ollama 目标应披露 %s 的丢弃，got losses=%v", want, fieldPaths(losses))
			}
		}
	})

	t.Run("gemini_tool_extras_disclosed_not_merged", func(t *testing.T) {
		req := minimalGeminiReq()
		req.Tools = []ToolDefinition{{
			Name:       "get_weather",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Extra:      json.RawMessage(`{"cache_control":{"type":"ephemeral"}}`),
		}}
		var out []byte
		losses := captureProtocolLoss(t, func() {
			out, _ = SerializeGemini(req)
		})
		if strings.Contains(string(out), "cache_control") {
			t.Fatalf("严格 proto3 body 不应并回工具 Extra: %s", out)
		}
		found := false
		for _, ev := range losses {
			if ev.FieldPath == "tools[].extra" {
				found = true
			}
		}
		if !found {
			t.Fatalf("工具 Extra 丢弃应披露，got %v", fieldPaths(losses))
		}
	})

	t.Run("gemini_structured_fields_disclosed", func(t *testing.T) {
		req := minimalGeminiReq()
		req.MCPServers = []MCPServer{{Name: "m"}}
		req.ContextManagement = &ContextManagement{}
		req.Container = &Container{}
		rp := 1.1
		req.RepetitionPenalty = &rp
		losses := captureProtocolLoss(t, func() {
			_, _ = SerializeGemini(req)
		})
		got := map[string]bool{}
		for _, ev := range losses {
			got[ev.FieldPath] = true
		}
		for _, want := range []string{"mcp_servers", "context_management", "container", "repetition_penalty"} {
			if !got[want] {
				t.Fatalf("gemini 目标应披露 %s，got %v", want, fieldPaths(losses))
			}
		}
	})

	t.Run("anthropic_seed_and_repetition_penalty_disclosed", func(t *testing.T) {
		req := minimalAnthropicReq()
		seed := int64(42)
		rp := 1.05
		req.Seed = &seed
		req.RepetitionPenalty = &rp
		losses := captureProtocolLoss(t, func() {
			_, _ = SerializeAnthropic(req)
		})
		got := map[string]bool{}
		for _, ev := range losses {
			got[ev.FieldPath] = true
		}
		if !got["seed"] || !got["repetition_penalty"] {
			t.Fatalf("anthropic 目标应披露 seed 与 repetition_penalty，got %v", fieldPaths(losses))
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// 最小请求构造 helper（只设被测字段，避免跨用例耦合）
// ─────────────────────────────────────────────────────────────────────────────

func minimalOllamaReq() *InternalRequest {
	return &InternalRequest{
		Model:    "qwen3:8b",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
}

func minimalGeminiReq() *InternalRequest {
	return &InternalRequest{
		Model:          "gemini-2.5-flash",
		SourceProtocol: ProtocolOpenAIChat,
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
}

func minimalAnthropicReq() *InternalRequest {
	return &InternalRequest{
		Model:          "claude-sonnet-5",
		MaxTokens:      1024,
		SourceProtocol: ProtocolOpenAIChat,
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
}

func mustOllama(t *testing.T, req *InternalRequest) map[string]any {
	t.Helper()
	out, err := SerializeOllama(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func fieldPaths(losses []AnomalyEvent) []string {
	out := make([]string, 0, len(losses))
	for _, ev := range losses {
		out = append(out, ev.FieldPath)
	}
	sort.Strings(out)
	return out
}
