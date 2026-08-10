package paramreg

import (
	"encoding/json"
	"testing"
)

// TestDecide_UnknownFieldAlwaysRestored 是本包最重要的一条不变量。
//
// 背景：修复前 serialize_openai.go:179 的 SourceProtocol 门禁使得
// Claude Code（anthropic）→ DeepSeek（openai-chat）这条主链路上，
// 客户端的一切未知字段全部丢失。Claude Code 的 CLAUDE_CODE_EXTRA_BODY
// 会把任意用户 JSON 展开进 body，必须能穿过网关。
func TestDecide_UnknownFieldAlwaysRestored(t *testing.T) {
	pairs := []struct{ src, dst Dialect }{
		{DialectAnthropic, DialectDeepSeek},
		{DialectAnthropic, DialectOpenAIChat},
		{DialectOpenAIChat, DialectAnthropic},
		{DialectOpenAIChat, DialectGemini},
		{DialectAnthropic, DialectGemini},
		{DialectGemini, DialectOpenAIChat},
		{DialectResponses, DialectAnthropic},
		{DialectUnknown, DialectUnknown},
	}
	for _, p := range pairs {
		action, spec := Decide("some_brand_new_vendor_param", p.src, p.dst)
		if action != ActionRestore {
			t.Errorf("%s→%s: unknown field action=%s, want restore", p.src, p.dst, action)
		}
		if spec != nil {
			t.Errorf("%s→%s: unknown field should have nil spec", p.src, p.dst)
		}
	}
}

func TestDecide_IRHandledRestored(t *testing.T) {
	// 2026-08-11: KindIRHandled 现在也返回 ActionRestore，不再是 ActionSkip。
	//
	// 原因：Extensions 里的字段必然是"某 parser 未消费的字段"。
	// reasoning / thinking_budget 等只有特定 dialect 的 parser 认识，
	// 在其它路径上会进 Extensions；ActionSkip 此时会静默丢弃。
	// ActionRestore 有"不覆盖已存在键"保护，故对 IR 真正处理的字段也是安全的。
	for _, key := range []string{"model", "messages", "temperature", "max_tokens", "tools"} {
		action, spec := Decide(key, DialectAnthropic, DialectOpenAIChat)
		if action != ActionRestore {
			t.Errorf("%s: action=%s, want restore", key, action)
		}
		if spec == nil {
			t.Fatalf("%s: expected registered spec", key)
		}
		if spec.Kind != KindIRHandled {
			t.Errorf("%s: kind=%d, want KindIRHandled", key, spec.Kind)
		}
	}
}

// TestDecide_DialectOnlyIsolation 验证方言私有字段不会跨方言泄漏。
//
// 这是保留隔离能力的关键：Mistral 的 additionalProperties:false 会拒绝
// 未知键，把 Anthropic 的 context_management 塞进去会硬 400。
func TestDecide_DialectOnlyIsolation(t *testing.T) {
	tests := []struct {
		key  string
		dst  Dialect
		want RestoreAction
	}{
		// Anthropic 私有 → 非 Anthropic 目标必须裁剪
		{"output_config", DialectMistral, ActionDrop},
		{"speed", DialectDeepSeek, ActionDrop},
		{"fallbacks", DialectOpenAIChat, ActionDrop},
		// Anthropic 私有 → Anthropic 目标必须还原
		{"output_config", DialectAnthropic, ActionRestore},
		{"speed", DialectAnthropic, ActionRestore},
		// vLLM 私有 → 其它 OpenAI 兼容厂商必须裁剪
		{"guided_json", DialectDeepSeek, ActionDrop},
		{"vllm_xargs", DialectGLM, ActionDrop},
		{"guided_json", DialectVLLM, ActionRestore},
		// MiniMax 私有
		{"reasoning_split", DialectGLM, ActionDrop},
		{"reasoning_split", DialectMiniMax, ActionRestore},
		// Gemini 私有
		{"labels", DialectOpenAIChat, ActionDrop},
		{"labels", DialectGemini, ActionRestore},
		// Ollama 字段是 KindPortable（Ollama 静默忽略不认识的字段，不会 400）：
		// 在目标为 DialectOllama 时还原，在其它目标也透传（portable 语义）。
		// 如需隔离，在 strip_request_fields 里配置。
		{"keep_alive", DialectOpenAIChat, ActionRestore}, // portable，不阻断
		{"keep_alive", DialectOllama, ActionRestore},
	}
	for _, tt := range tests {
		action, _ := Decide(tt.key, DialectUnknown, tt.dst)
		if action != tt.want {
			t.Errorf("Decide(%q, →%s) = %s, want %s", tt.key, tt.dst, action, tt.want)
		}
	}
}

// TestDecide_RejectedByWinsOverEverything 验证硬报错防护的最高优先级。
//
// Grok 推理模型收到 presence_penalty / frequency_penalty / stop 会直接报错，
// 即使这些是标准 OpenAI 字段也必须裁剪。
func TestDecide_RejectedByWinsOverEverything(t *testing.T) {
	for _, key := range []string{"presence_penalty", "frequency_penalty", "stop"} {
		action, spec := Decide(key, DialectOpenAIChat, DialectGrok)
		if action != ActionDrop {
			t.Errorf("Decide(%q, →grok) = %s, want drop (Grok 推理模型硬报错)", key, action)
		}
		if spec == nil {
			t.Fatalf("%s: expected registered spec", key)
		}
	}
	// 同样的字段到别的方言应放行（这里是 IR 已处理 → skip）
	action, _ := Decide("presence_penalty", DialectOpenAIChat, DialectDeepSeek)
	if action == ActionDrop {
		t.Error("presence_penalty → deepseek 不该 drop")
	}
}

// TestDecide_PortableCrossesDialects 验证通用语义字段可跨方言。
func TestDecide_PortableCrossesDialects(t *testing.T) {
	// repetition_penalty 网关此前完全无处理（审计 1.6），
	// 现登记为 Portable，多家厂商都认。
	for _, dst := range []Dialect{DialectQwen, DialectGLM, DialectArk, DialectVLLM, DialectOpenAIChat} {
		action, _ := Decide("repetition_penalty", DialectAnthropic, dst)
		if action != ActionRestore {
			t.Errorf("repetition_penalty → %s = %s, want restore", dst, action)
		}
	}
	// stream_options 是 IR 零处理的关键字段（fields.go:8-13 事故），
	// 必须靠透传，不能被判成 skip。
	action, spec := Decide("stream_options", DialectOpenAIChat, DialectDeepSeek)
	if action != ActionRestore {
		t.Errorf("stream_options = %s, want restore（IR 不处理该字段，须透传）", action)
	}
	if spec == nil || spec.Kind != KindPortable {
		t.Error("stream_options 必须是 KindPortable —— 标成 KindIRHandled 会重演静默丢失事故")
	}
	// betas 是 Claude Code 的身份标识，取值每月变动，必须原样透传。
	if action, _ := Decide("betas", DialectAnthropic, DialectAnthropic); action != ActionRestore {
		t.Errorf("betas = %s, want restore", action)
	}
}

// TestDecide_UnknownDstIsPermissive 验证未识别目标方言走最宽松路径。
//
// 新接入的厂商不该因为没登记 catalog code 就丢参数。
func TestDecide_UnknownDstIsPermissive(t *testing.T) {
	for _, key := range []string{"output_config", "guided_json", "labels", "keep_alive"} {
		action, _ := Decide(key, DialectAnthropic, DialectUnknown)
		if action != ActionRestore {
			t.Errorf("Decide(%q, →unknown) = %s, want restore（未知目标须宽松）", key, action)
		}
	}
	// 但 RejectedBy 不受 unknown 影响 —— 未知目标不等于"确定不拒绝"，
	// 这里 unknown 不在 RejectedBy 列表里所以放行，符合预期。
	if action, _ := Decide("stop", DialectOpenAIChat, DialectUnknown); action == ActionDrop {
		t.Error("stop → unknown 不该 drop")
	}
}

func TestDecide_TranslateRandomSeed(t *testing.T) {
	val := json.RawMessage(`42`)

	key, out, action, _ := Apply("random_seed", val, DialectMistral, DialectMistral)
	if action != ActionTranslate || key != "random_seed" || string(out) != "42" {
		t.Errorf("→mistral: key=%q action=%s val=%s", key, action, out)
	}

	key, out, action, _ = Apply("random_seed", val, DialectMistral, DialectOpenAIChat)
	if action != ActionTranslate || key != "seed" || string(out) != "42" {
		t.Errorf("→openai: key=%q action=%s val=%s, want seed/translate/42", key, action, out)
	}
}

// TestDialectInheritance 验证 OpenAI 兼容厂商能认识 OpenAI Chat 标准字段。
func TestDialectInheritance(t *testing.T) {
	if !IsOpenAIShaped(DialectDeepSeek) {
		t.Error("DeepSeek 应是 OpenAI 线格式")
	}
	if IsOpenAIShaped(DialectAnthropic) {
		t.Error("Anthropic 不是 OpenAI 线格式")
	}
	if BaseProtocol(DialectGemini) != DialectGemini {
		t.Error("一等协议方言的 BaseProtocol 应是自身")
	}

	// moderation 登记为 openai_chat/responses 私有；DeepSeek 底层是
	// openai_chat，因此继承认识它。
	if action, _ := Decide("moderation", DialectOpenAIChat, DialectDeepSeek); action != ActionRestore {
		t.Errorf("moderation → deepseek = %s, want restore（方言继承）", action)
	}
	// 但 Anthropic 不是 OpenAI 形态，应裁剪。
	if action, _ := Decide("moderation", DialectOpenAIChat, DialectAnthropic); action != ActionDrop {
		t.Error("moderation → anthropic 应 drop")
	}
}

func TestDialectResolve(t *testing.T) {
	tests := []struct {
		catalog, protocol string
		want              Dialect
	}{
		{"deepseek", "openai-chat", DialectDeepSeek},
		{"zhipu", "openai-chat", DialectGLM}, // 别名
		{"bigmodel", "", DialectGLM},         // 别名
		{"volcengine", "", DialectArk},       // 别名
		{"moonshot", "", DialectKimi},        // 别名
		{"", "anthropic-messages", DialectAnthropic},
		{"", "gemini-generate", DialectGemini},
		{"", "openai-responses", DialectResponses},
		{"totally-new-vendor", "openai-chat", DialectOpenAIChat}, // 回退到协议
		{"", "", DialectUnknown},
	}
	for _, tt := range tests {
		if got := Resolve(tt.catalog, tt.protocol); got != tt.want {
			t.Errorf("Resolve(%q,%q) = %q, want %q", tt.catalog, tt.protocol, got, tt.want)
		}
	}
}

// TestKnownFieldsForDialect 验证白名单足够宽 —— 修复 sanitizer 的地雷。
//
// 原 alwaysKeepFieldsOpenAI 只有 8 个字段，白名单模式下会删掉
// tools / tool_choice / stream_options / response_format 等关键字段。
func TestKnownFieldsForDialect(t *testing.T) {
	openai := KnownFieldsForDialect(DialectOpenAIChat)
	for _, key := range []string{
		"model", "messages", "tools", "tool_choice", "stream_options",
		"response_format", "reasoning_effort", "parallel_tool_calls",
		"logit_bias", "seed", "metadata",
	} {
		if !openai[key] {
			t.Errorf("OpenAI 白名单缺少关键字段 %q", key)
		}
	}
	if len(openai) < 50 {
		t.Errorf("OpenAI 白名单只有 %d 个字段，过窄（原 sanitizer 8 字段的地雷）", len(openai))
	}
	// Anthropic 白名单应含其私有字段，且不含 vLLM 私有字段。
	anth := KnownFieldsForDialect(DialectAnthropic)
	for _, key := range []string{"system", "thinking", "cache_control", "output_config", "betas"} {
		if !anth[key] {
			t.Errorf("Anthropic 白名单缺少 %q", key)
		}
	}
	if anth["guided_json"] {
		t.Error("Anthropic 白名单不该含 vLLM 私有字段 guided_json")
	}
	// Grok 白名单不该含它会硬报错的字段。
	grok := KnownFieldsForDialect(DialectGrok)
	for _, key := range []string{"presence_penalty", "frequency_penalty", "stop"} {
		if grok[key] {
			t.Errorf("Grok 白名单不该含硬报错字段 %q", key)
		}
	}
}

// TestIRHandledFields 验证 IR 已处理字段集合的正确性。
func TestIRHandledFields(t *testing.T) {
	f := IRHandledFields()
	for _, key := range []string{"model", "messages", "temperature", "max_tokens", "reasoning_effort", "thinking"} {
		if !f[key] {
			t.Errorf("IRHandledFields 缺少 %q", key)
		}
	}
	// 这两个是 IR 零处理的，绝不能标成 IR 已处理，否则重演静默丢失事故。
	for _, key := range []string{"stream_options", "metadata"} {
		if f[key] {
			t.Errorf("%q 被误标为 IR 已处理 —— 见 fields.go:8-13 的事故记录", key)
		}
	}
}

// TestRegistryIntegrity 是注册表自身的一致性检查。
func TestRegistryIntegrity(t *testing.T) {
	seen := make(map[string]string)
	for i := range specs {
		s := &specs[i]
		if s.Name == "" {
			t.Errorf("specs[%d] 缺少 Name", i)
			continue
		}
		key := normalizeKey(s.Name)
		if prev, dup := seen[key]; dup {
			t.Errorf("字段 %q 重复登记（前一次为 %q）", s.Name, prev)
		}
		seen[key] = s.Name

		if s.Kind == KindIRHandled && s.IRPath == "" {
			t.Errorf("%q 标为 KindIRHandled 但未填 IRPath（审计需要）", s.Name)
		}
		if s.Kind == KindDialectOnly && len(s.Dialects) == 0 {
			t.Errorf("%q 标为 KindDialectOnly 但 Dialects 为空 —— 会导致对所有目标都 drop", s.Name)
		}
		if s.Kind == KindTranslatable && s.Translate == nil {
			t.Errorf("%q 标为 KindTranslatable 但未提供 Translate", s.Name)
		}
	}
	if len(RegisteredNames()) != len(specs) {
		t.Error("RegisteredNames 与 specs 数量不一致")
	}
}
