package paramreg

import (
	"encoding/json"
	"testing"
)

// 2026-11-01 参数兼容静态化轮（跨端点实机矩阵，四腿：OpenRouter / zhipu /
// volcengine-Ark / nvidia，证据与读数见
// docs/audit/2026-11-01-参数兼容静态化轮-跨端点reasoning实证.md）：
//
//   - chat 线上顶层 reasoning 对象不是普遍被拒：nvidia NIM 硬 400
//     （生产 reqanom 台账 2026-10-28~31）、zhipu/Ark 静默忽略、
//     OpenRouter 原生接受且真生效；
//   - reasoning_effort 下划线在全部四腿都生效 ⇒ 双表达同发只保留
//     下划线是安全的（无供应商只认对象形态）；
//   - 对象可携带下划线表达不了的 OpenRouter 旋钮（exclude/max_tokens/
//     enabled）⇒ 双表达同发时只剥 effort、保留旋钮。
//
// 本文件钉死 ReasoningObjectChatDecision 的方言矩阵与登记行的
// RejectedBy（r1021 教义：登记行即硬拒的事实源，变异臂据此设靶）。

// TestNVIDIADialectRegistered 钉 nvidia 方言的登记链：dialect 常量、
// 底层协议、catalog code 映射三者齐全。漏任何一环 catalog "nvidia" 都会
// 退回 DialectUnknown fail-open，硬拒登记整体失效。
func TestNVIDIADialectRegistered(t *testing.T) {
	if DialectNVIDIA != "nvidia" {
		t.Fatalf("DialectNVIDIA = %q, want nvidia", DialectNVIDIA)
	}
	if BaseProtocol(DialectNVIDIA) != DialectOpenAIChat {
		t.Fatalf("nvidia BaseProtocol = %q, want openai_chat", BaseProtocol(DialectNVIDIA))
	}
	if got := DialectForCatalogCode("nvidia"); got != DialectNVIDIA {
		t.Fatalf("DialectForCatalogCode(nvidia) = %q, want nvidia dialect（catalog 映射缺失 = 硬拒登记失效）", got)
	}
	found := false
	for _, d := range AllDialects {
		if d == DialectNVIDIA {
			found = true
		}
	}
	if !found {
		t.Fatal("AllDialects 缺 nvidia")
	}
}

// TestReasoningRejectedByNVIDIA 钉 registry reasoning 行的 RejectedBy：
// nvidia NIM 对顶层 reasoning 对象硬 400（生产台账四天连证），必须是
// 第一优先级硬拒。变异臂 M1：从 RejectedBy 摘掉 DialectNVIDIA ⇒ 本测试红。
func TestReasoningRejectedByNVIDIA(t *testing.T) {
	spec := Lookup("reasoning")
	if spec == nil {
		t.Fatal("reasoning 未登记")
	}
	if !spec.rejects(DialectNVIDIA) {
		t.Fatal("reasoning 必须登记 RejectedBy: nvidia（NIM 400 有四天生产台账实证）")
	}
	// 保留方言不受硬拒波及：OpenRouter 原生、Unknown fail-open。
	if spec.rejects(DialectOpenRouter) || spec.rejects(DialectUnknown) {
		t.Fatal("reasoning 的 RejectedBy 不得波及 openrouter/unknown")
	}
}

// TestThinkingFamilyRejectedByNVIDIA 钉 thinking / enable_thinking 的
// nvidia 硬拒：方言登记后 dst 不再落 Unknown，RejectedByUnknown 对 nvidia
// 失效；不补登记会把 unknown-relay 时代已裁剪的字段重新放进 nvidia
// 请求体（回归——NIM 400「Unsupported parameter(s): `enable_thinking`」
// 生产台账实证）。变异臂 M2：摘掉任一条 ⇒ 本测试红。
func TestThinkingFamilyRejectedByNVIDIA(t *testing.T) {
	if spec := Lookup("thinking"); spec == nil || !spec.rejects(DialectNVIDIA) {
		t.Fatal("thinking 必须登记 RejectedBy: nvidia（方言登记后 RejectedByUnknown 对 nvidia 失效）")
	}
	if spec := Lookup("enable_thinking"); spec == nil || !spec.rejects(DialectNVIDIA) {
		t.Fatal("enable_thinking 必须登记 RejectedBy: nvidia")
	}
}

// TestReasoningObjectChatDecision_Matrix 钉 chat 线对象裁决矩阵。
func TestReasoningObjectChatDecision_Matrix(t *testing.T) {
	obj := func(s string) json.RawMessage { return json.RawMessage(s) }

	t.Run("nvidia 硬拒（registry RejectedBy 裁决）", func(t *testing.T) {
		key, _, act, reason := ReasoningObjectChatDecision(obj(`{"effort":"high"}`), false, DialectNVIDIA)
		if act != ActionDrop || reason != "rejected_by_target_dialect" {
			t.Fatalf("nvidia 应 ActionDrop/rejected_by_target_dialect, got %v/%q", act, reason)
		}
		if key != "" {
			t.Fatal("drop 时不得返回写入键")
		}
	})

	// 变异臂 M3：翻译方言集漏 GLM/Ark ⇒ 下标臂红。
	t.Run("无下划线：GLM/Ark/DeepSeek/Grok/OpenAIChat 翻译成 reasoning_effort", func(t *testing.T) {
		for _, dst := range []Dialect{DialectGLM, DialectArk, DialectDeepSeek, DialectGrok, DialectOpenAIChat} {
			key, val, act, _ := ReasoningObjectChatDecision(obj(`{"effort":"high"}`), false, dst)
			if act != ActionRestore || key != "reasoning_effort" {
				t.Fatalf("dst=%s 应翻译成 reasoning_effort, got %v %q", dst, act, key)
			}
			if string(val) != `"high"` {
				t.Fatalf("dst=%s effort 值应原样带引号, got %s", dst, val)
			}
		}
	})

	t.Run("无下划线：OpenRouter/Unknown/其余方言保留对象（今日语义零回归）", func(t *testing.T) {
		for _, dst := range []Dialect{DialectOpenRouter, DialectUnknown, DialectMiniMax, DialectKimi, DialectQwen, DialectMistral, DialectVLLM, DialectOllama} {
			key, val, act, _ := ReasoningObjectChatDecision(obj(`{"effort":"high"}`), false, dst)
			if act != ActionRestore || key != "reasoning" || string(val) != `{"effort":"high"}` {
				t.Fatalf("dst=%s 应原样保留对象, got %v %q %s", dst, act, key, val)
			}
		}
	})

	t.Run("双表达同发且对象 effort-only：静默收口（下划线处处生效）", func(t *testing.T) {
		for _, dst := range []Dialect{DialectOpenRouter, DialectGLM, DialectArk, DialectUnknown} {
			_, _, act, _ := ReasoningObjectChatDecision(obj(`{"effort":"high"}`), true, dst)
			if act != ActionSkip {
				t.Fatalf("dst=%s effort-only 双表达应 ActionSkip, got %v", dst, act)
			}
		}
	})

	// 变异臂 M4：双表达旋钮保留逻辑被改坏（整对象丢弃或 effort 未剥）⇒ 本臂红。
	t.Run("双表达同发且对象带旋钮：剥 effort 留旋钮（OpenRouter exclude 语义不丢、冲突 400 类消除）", func(t *testing.T) {
		key, val, act, _ := ReasoningObjectChatDecision(obj(`{"effort":"low","exclude":true}`), true, DialectOpenRouter)
		if act != ActionRestore || key != "reasoning" {
			t.Fatalf("应保留旋钮对象, got %v %q", act, key)
		}
		var m map[string]any
		if err := json.Unmarshal(val, &m); err != nil {
			t.Fatalf("reduced object 非法 JSON: %v", err)
		}
		if _, has := m["effort"]; has {
			t.Fatalf("旋钮对象必须剥掉 effort（OpenRouter 冲突值 400 类）: %s", val)
		}
		if m["exclude"] != true {
			t.Fatalf("exclude 旋钮必须保留: %s", val)
		}
	})

	t.Run("无下划线且对象无 effort（旋钮-only）进翻译方言集：Drop 上报 loss", func(t *testing.T) {
		_, _, act, reason := ReasoningObjectChatDecision(obj(`{"exclude":true}`), false, DialectGLM)
		if act != ActionDrop || reason == "" {
			t.Fatalf("翻译方言集上的旋钮-only 对象应 Drop 并带 loss 理由, got %v %q", act, reason)
		}
		// 保留方言集维持原样透传（今日语义）。
		key, val, act, _ := ReasoningObjectChatDecision(obj(`{"exclude":true}`), false, DialectOpenRouter)
		if act != ActionRestore || key != "reasoning" || string(val) != `{"exclude":true}` {
			t.Fatalf("OpenRouter 旋钮-only 对象应原样保留, got %v %q %s", act, key, val)
		}
	})

	t.Run("非对象标量：翻译方言集 Drop、保留方言集原样", func(t *testing.T) {
		if _, _, act, _ := ReasoningObjectChatDecision(obj(`"high"`), false, DialectGLM); act != ActionDrop {
			t.Fatalf("标量进翻译集应 Drop, got %v", act)
		}
		key, val, act, _ := ReasoningObjectChatDecision(obj(`"high"`), false, DialectOpenRouter)
		if act != ActionRestore || key != "reasoning" || string(val) != `"high"` {
			t.Fatalf("标量进保留集应原样透传, got %v %q %s", act, key, val)
		}
	})
}
