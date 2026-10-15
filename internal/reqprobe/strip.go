package reqprobe

import (
	"encoding/json"
	"strings"
)

// coreParams 是无名兜底剥参（未点名参数时的 uncommon 清单交集）永不触碰
// 的参数面：核心采样/骨架参数一概不动，只有确属"不常见"的才拿掉。
var coreParams = map[string]bool{
	"model":                 true,
	"messages":              true,
	"input":                 true,
	"stream":                true,
	"max_tokens":            true,
	"max_completion_tokens": true,
	"temperature":           true,
	"top_p":                 true,
	"tools":                 true,
	"tool_choice":           true,
	"stop":                  true,
	// stream_options 控制流式 usage 回传，剔除会破坏 usage 采集；
	// 它几乎不被上游拒绝（Groq/DeepSeek 等都接受 extra body），保守保留。
	"stream_options": true,
}

// skeletonAlways 是点名路径下**所有线格式**都保护的结构骨架。
//
// 2026-10-13（Vapeur gpt-5.6-sol temperature 事故）：此前点名路径也复用
// coreParams，temperature 被保护 → 诊断器点名成功、剥参空手 → 三连 400。
// 上游已用文案背书「该参数发不得」时，剥掉采样参数是正确修正。仍不剥的
// 只有会让请求无法成形或静默改传输/功能语义的骨架。
//
// P0-B（2026-10-14 magpie 对标）：stream_options 从「全局骨架」拆到
// skeletonChatOnly——openai-chat 线保护（usage 回传契约），responses /
// anthropic / gemini 线可剥（那些线格式根本没有该字段；r1026 三腿泄漏
// 的根因就是全局保护挡住了 reqprobe，逼出平行的 paramrefusal 记忆）。
var skeletonAlways = map[string]bool{
	"model":       true,
	"messages":    true,
	"input":       true,
	"stream":      true,
	"tools":       true,
	"tool_choice": true,
}

// skeletonChatOnly 仅 openai-chat（及未指名线格式的保守默认）保护。
var skeletonChatOnly = map[string]bool{
	"stream_options": true,
}

// isSkeleton 按线格式判定点名路径下是否仍保护该键。
// wire 空 / openai-chat / openai-completions → chat 族（含 stream_options）；
// openai-responses / anthropic-messages / gemini* → 仅 skeletonAlways。
func isSkeleton(key, wire string) bool {
	if skeletonAlways[key] {
		return true
	}
	if !skeletonChatOnly[key] {
		return false
	}
	w := strings.ToLower(strings.TrimSpace(wire))
	switch {
	case w == "" || w == "openai-chat" || w == "openai-completions" || w == "openai":
		return true
	default:
		return false
	}
}

// skeletonParams 保留给既有测试/注释引用：等价于 chat 线骨架（最保守）。
// 新代码请用 isSkeleton(key, wire)。
var skeletonParams = func() map[string]bool {
	m := map[string]bool{}
	for k := range skeletonAlways {
		m[k] = true
	}
	for k := range skeletonChatOnly {
		m[k] = true
	}
	return m
}()

// uncommonParams 是"不确定就先拿掉再试"的候选清单，按常见度粗排：
// 推理控制最常出问题（各家方言互不兼容），其次是采样微调与平台特有键。
var uncommonParams = []string{
	"reasoning_effort",
	"reasoning",
	"thinking",
	"thinking_budget",
	"enable_thinking",
	"effort",
	"verbosity",
	"top_k",
	"frequency_penalty",
	"presence_penalty",
	"seed",
	"logprobs",
	"logit_bias",
	"service_tier",
	"parallel_tool_calls",
	"prediction",
	"web_search",
	"web_search_options",
	"response_format",
	"metadata",
	"user",
	"store",
	"reasoning_effort_hint",
}

// wireKeyAliases 把上游文案点名的参数名映射到我方出站线体的顶层键。
//
// 2026-10-14（Bedrock 后端 effort 值拒绝，claude-opus-5-5 经 vapeur）：
// 上游侧叫 "output_config.effort"（Bedrock converse 概念），我方 chat
// 线体键是 "reasoning_effort"、responses 线体嵌套在 "reasoning"。不映射
// 则 named 路径空手而归 → 剥参重试不触发（同名假设首次被上游命名空间
// 打破）。嵌套键（reasoning.effort）剥整对象：effort 是该对象的全部
// 语义，留空对象不如整键删除。
var wireKeyAliases = map[string][]string{
	"output_config.effort": {"reasoning_effort", "reasoning"},
	"reasoning.effort":     {"reasoning_effort", "reasoning"},
	"effort":               {"reasoning_effort", "reasoning"},
}

// resolveWireKeys 返回 named 参数名在出站体上可能的线体键
// （原名 + 别名扩展，保持原名优先）。
func resolveWireKeys(named string) []string {
	named = strings.ToLower(strings.TrimSpace(named))
	keys := []string{named}
	keys = append(keys, wireKeyAliases[named]...)
	return keys
}

// StrippableParams 返回出站体中存在且可剔除的参数名列表（wire 空 = chat 保守骨架）。
func StrippableParams(outbound []byte, named string) []string {
	return StrippableParamsForWire(outbound, named, "")
}

// StrippableParamsForWire 同 StrippableParams，骨架按线格式判定（P0-B）。
func StrippableParamsForWire(outbound []byte, named, wire string) []string {
	obj := parseOutboundTopLevel(outbound)
	if obj == nil {
		return nil
	}
	if named != "" {
		for _, n := range strings.Split(named, ",") {
			for _, k := range resolveWireKeys(n) {
				if k == "" || isSkeleton(k, wire) {
					continue
				}
				if _, ok := obj[k]; ok {
					return []string{k}
				}
			}
		}
		return nil
	}
	out := make([]string, 0, len(uncommonParams))
	for _, p := range uncommonParams {
		if _, ok := obj[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// StripParams 从出站体剔除参数（wire 空 = chat 保守骨架）。
func StripParams(body []byte, named string) ([]byte, []string) {
	return StripParamsForWire(body, named, "")
}

// StripParamsForWire 同 StripParams，骨架按线格式判定（P0-B）。
func StripParamsForWire(body []byte, named, wire string) ([]byte, []string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body, nil
	}
	var targets []string
	if named != "" {
		for _, n := range strings.Split(named, ",") {
			for _, k := range resolveWireKeys(n) {
				if k == "" || isSkeleton(k, wire) {
					continue
				}
				if _, ok := obj[k]; ok {
					targets = append(targets, k)
				}
			}
		}
	} else {
		for _, p := range uncommonParams {
			if _, ok := obj[p]; ok {
				targets = append(targets, p)
			}
		}
	}
	if len(targets) == 0 {
		return body, nil
	}
	for _, t := range targets {
		delete(obj, t)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body, nil
	}
	return out, targets
}
