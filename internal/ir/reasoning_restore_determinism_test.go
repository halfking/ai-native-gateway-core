package ir

import (
	"encoding/json"
	"testing"
)

// 2026-11-01 12h 审计 P1-1 / P1-2（extensions_restore 出站确定性与
// OpenRouter 冲突 400）：
//
// 触发形态 = **下划线与对象双表达都落在 Extensions**。若下划线被 parser
// 消费进 IR（chat 入向走 parse_openai），出站体在进入 restore 时就已带
// reasoning_effort ⇒ 与 map 迭代序无关；真正的顺序依赖出现在「下划线由
// 通用路径在循环内写回」的形态（anthropic/responses 入向、IR 未渲染）。
//
// 旧实现把「出站是否已有下划线」这个探测放在 map 循环体内
// （extensions_restore 的 reasoning 特例）⇒ 同一请求出站 body 随迭代序
// 变化（HEAD 实测 300 次出站出现 245/55 混合；基线 300/300 恒定），
// 且 OpenRouter「对象内 effort 与 reasoning_effort 同发」的冲突 400
// 只被消掉 78~80%（实测 3 次复跑 80.2% / 79.3% / 78.5%）。
//
// 本文件钉三件事：
//   ① 同一输入 N 次序列化 ⇒ 字节逐字相同（出站可复现）；
//   ② 出站 body 不得同时含「对象内 effort」与「顶层 reasoning_effort」
//      （OpenRouter conflicting values 400 的静态消除）；
//   ③ 客户端显式 effort 优先于 thinking 换算（既有优先级的落地）。

// determinismShape 是一条待序列化的出站形态。
type determinismShape struct {
	name   string
	target string
	exts   map[string]json.RawMessage
}

// determinismShapes 覆盖「两个来源写同一个出站键」的全部组合：
// 下划线×对象、thinking×对象、thinking×下划线×对象。
var determinismShapes = []determinismShape{
	{
		name:   "双表达（下划线+对象旋钮）→ openrouter",
		target: "openrouter",
		exts: map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"low"`),
			"reasoning":        json.RawMessage(`{"effort":"high","exclude":true,"max_tokens":512}`),
		},
	},
	{
		name:   "双表达（下划线+effort-only 对象）→ openrouter",
		target: "openrouter",
		exts: map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"low"`),
			"reasoning":        json.RawMessage(`{"effort":"high"}`),
		},
	},
	{
		name:   "双表达（下划线+对象）→ zhipu（翻译方言集）",
		target: "zhipu",
		exts: map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"low"`),
			"reasoning":        json.RawMessage(`{"effort":"high","exclude":true}`),
		},
	},
	{
		name:   "thinking + 对象 effort → unknown-relay（显式 effort 优先）",
		target: "some-unregistered-relay",
		exts: map[string]json.RawMessage{
			"thinking":  json.RawMessage(`{"type":"enabled","budget_tokens":1024}`),
			"reasoning": json.RawMessage(`{"effort":"high"}`),
		},
	},
	{
		name:   "thinking + 对象旋钮（无 effort）→ unknown-relay",
		target: "some-unregistered-relay",
		exts: map[string]json.RawMessage{
			"thinking":  json.RawMessage(`{"type":"enabled","budget_tokens":1024}`),
			"reasoning": json.RawMessage(`{"exclude":true}`),
		},
	},
	{
		name:   "thinking + 下划线 + 对象 → unknown-relay",
		target: "some-unregistered-relay",
		exts: map[string]json.RawMessage{
			"thinking":         json.RawMessage(`{"type":"enabled","budget_tokens":1024}`),
			"reasoning_effort": json.RawMessage(`"low"`),
			"reasoning":        json.RawMessage(`{"effort":"high"}`),
		},
	},
	{
		name:   "thinking + 对象 → grok（通用 thinking 翻译也写 reasoning_effort）",
		target: "grok",
		exts: map[string]json.RawMessage{
			"thinking":  json.RawMessage(`{"type":"enabled","budget_tokens":1024}`),
			"reasoning": json.RawMessage(`{"effort":"high"}`),
		},
	},
	{
		name:   "下划线 + 对象 → nvidia（硬拒方言）",
		target: "nvidia",
		exts: map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"low"`),
			"reasoning":        json.RawMessage(`{"effort":"high"}`),
		},
	},
}

// buildDeterminismReq 构造一条 chat 入向 IR，并把形态注入 Extensions。
func buildDeterminismReq(t *testing.T, s determinismShape) *InternalRequest {
	t.Helper()
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`
	req, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	for k, v := range s.exts {
		req.Extensions[k] = v
	}
	req.TargetProvider = s.target
	return req
}

// TestRestoreExtensions_OutboundBodyIsDeterministic 出站可复现性：
// 同一输入序列化 N 次必须字节逐字相同。
//
// 变异臂靶：把 extensions_restore 的「出站是否已有下划线」探测改回
// 循环体内（读可变的 out）⇒ 本文件红（出站随 map 迭代序变化）。
func TestRestoreExtensions_OutboundBodyIsDeterministic(t *testing.T) {
	const runs = 50
	for _, s := range determinismShapes {
		s := s
		t.Run(s.name, func(t *testing.T) {
			req := buildDeterminismReq(t, s)
			first, err := SerializeOpenAI(req)
			if err != nil {
				t.Fatalf("SerializeOpenAI: %v", err)
			}
			for i := 0; i < runs; i++ {
				got, err := SerializeOpenAI(req)
				if err != nil {
					t.Fatalf("SerializeOpenAI #%d: %v", i, err)
				}
				if string(got) != string(first) {
					t.Fatalf("第 %d 次序列化与首次不同 ⇒ 出站 body 不可复现\n首次: %s\n本次: %s", i+1, first, got)
				}
			}
		})
	}
}

// TestRestoreExtensions_NeverBothObjectEffortAndUnderscore 出站不变量：
// body 不得同时含「reasoning 对象内的 effort」与「顶层 reasoning_effort」
// —— OpenRouter 对这两种同发直接报 conflicting values 400。
//
// 窗口内那次改动的宣称是「冲突值 400 类被静态消除」，而实测只消掉
// 78~80%（剩下的取决于 map 迭代序）。本测试把宣称变成判据。
func TestRestoreExtensions_NeverBothObjectEffortAndUnderscore(t *testing.T) {
	for _, s := range determinismShapes {
		s := s
		t.Run(s.name, func(t *testing.T) {
			req := buildDeterminismReq(t, s)
			for i := 0; i < 20; i++ {
				body, err := SerializeOpenAI(req)
				if err != nil {
					t.Fatalf("SerializeOpenAI #%d: %v", i, err)
				}
				var out map[string]any
				if err := json.Unmarshal(body, &out); err != nil {
					t.Fatalf("unmarshal outbound body: %v", err)
				}
				_, hasUnderscore := out["reasoning_effort"]
				obj, hasObj := out["reasoning"].(map[string]any)
				if !hasObj {
					continue
				}
				_, objHasEffort := obj["effort"]
				if objHasEffort && hasUnderscore {
					t.Fatalf("第 %d 次出站同时含对象内 effort 与顶层 reasoning_effort ⇒ OpenRouter conflicting values 400：%s",
						i+1, body)
				}
			}
		})
	}
}

// TestRestoreExtensions_ExplicitEffortBeatsThinkingConversion 优先级落地：
// 客户端显式表达的 effort（对象内）优先于 thinking 换算出来的值。
//
// 旧实现里这层优先级是**碰运气**的（thinking 与 reasoning 谁先被遍历到
// 决定胜者）⇒ 同一请求可能把客户端显式的 high 换成 medium。
func TestRestoreExtensions_ExplicitEffortBeatsThinkingConversion(t *testing.T) {
	req := buildDeterminismReq(t, determinismShape{
		target: "some-unregistered-relay",
		exts: map[string]json.RawMessage{
			"thinking":  json.RawMessage(`{"type":"enabled","budget_tokens":1024}`),
			"reasoning": json.RawMessage(`{"effort":"high"}`),
		},
	})
	body, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal outbound body: %v", err)
	}
	obj, ok := out["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("对象形态必须原样保留（OpenRouter 原生生效方言，零回归）: %s", body)
	}
	if obj["effort"] != "high" {
		t.Fatalf("客户端显式 effort=high 不得被 thinking 换算顶掉: %s", body)
	}
	if _, exists := out["reasoning_effort"]; exists {
		t.Fatalf("thinking 换算不得在客户端显式 effort 之外再占一个下划线槽位: %s", body)
	}
}

// TestRestoreExtensions_DualSpellingKeepsUnderscoreAndKnobs 静态裁决的正面判据：
// 双表达同发 ⇒ 下划线保留、对象内 effort 让位、旋钮保留。
func TestRestoreExtensions_DualSpellingKeepsUnderscoreAndKnobs(t *testing.T) {
	req := buildDeterminismReq(t, determinismShape{
		target: "openrouter",
		exts: map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"low"`),
			"reasoning":        json.RawMessage(`{"effort":"high","exclude":true,"max_tokens":512}`),
		},
	})
	body, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal outbound body: %v", err)
	}
	if out["reasoning_effort"] != "low" {
		t.Fatalf("客户端显式下划线必须保留: %s", body)
	}
	obj, ok := out["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("旋钮对象必须保留: %s", body)
	}
	if _, has := obj["effort"]; has {
		t.Fatalf("对象内 effort 必须让位给下划线: %v", obj)
	}
	if obj["exclude"] != true || obj["max_tokens"] != float64(512) {
		t.Fatalf("旋钮必须原样保留: %v", obj)
	}
}
