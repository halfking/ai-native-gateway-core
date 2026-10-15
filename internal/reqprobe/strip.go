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

// skeletonParams 是点名路径（上游 4xx 文案明确点出参数名）下仍受保护的
// 结构骨架。比 coreParams 窄得多：
//
// 2026-10-13（Vapeur gpt-5.6-sol temperature 事故）：此前点名路径也复用
// coreParams，temperature 被保护 → Vapeur /v1/responses 对推理家族明确
// 回 400 "Unsupported parameter: 'temperature' is not supported with
// this model."，诊断器点名成功、剥参却拿不出可剥清单 → 免费重试不触发，
// 外层按 transient 把同一 body 原样重发三连 400 → 503。上游已经用错误
// 文案背书"该参数发不得"，此时剥掉采样参数（temperature/top_p/stop/
// max_tokens…）是正确的确定性修正：模型侧默认值兜底，请求语义不损。
// 仍然不剥的只有会让请求无法成形（model/messages/input）、或静默改变
// 传输与功能语义（stream 改传输形态、tools/tool_choice 关掉函数调用、
// stream_options 破坏 usage 采集）的骨架——那些"成功"是假成功。
var skeletonParams = map[string]bool{
	"model":          true,
	"messages":       true,
	"input":          true,
	"stream":         true,
	"tools":          true,
	"tool_choice":    true,
	"stream_options": true,
}

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

// StrippableParams 返回出站体中存在且可剔除的参数名列表。
// named 非空时只返回它（若存在）；否则返回 uncommon 清单与出站体的交集。
// named 路径只让开 skeletonParams（见其注释）；无名路径 coreParams 永不返回。
func StrippableParams(outbound []byte, named string) []string {
	obj := parseOutboundTopLevel(outbound)
	if obj == nil {
		return nil
	}
	if named != "" {
		for _, n := range strings.Split(named, ",") {
			n = strings.ToLower(strings.TrimSpace(n))
			if n == "" || skeletonParams[n] {
				continue
			}
			if _, ok := obj[n]; ok {
				return []string{n}
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

// StripParams 从出站体剔除参数并返回新 body 与实际剔除清单。
// body 无效/没有可剔除参数时返回原 body 与 nil。
func StripParams(body []byte, named string) ([]byte, []string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body, nil
	}
	var targets []string
	if named != "" {
		for _, n := range strings.Split(named, ",") {
			n = strings.ToLower(strings.TrimSpace(n))
			if n == "" || skeletonParams[n] {
				continue
			}
			if _, ok := obj[n]; ok {
				targets = append(targets, n)
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
