package reqprobe

import (
	"encoding/json"
	"strings"
)

// coreParams 是任何 OpenAI 形态请求的骨架参数，剔除逻辑永不触碰。
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
	"web_search_options",
	"response_format",
	"metadata",
	"user",
	"store",
	"reasoning_effort_hint",
}

// StrippableParams 返回出站体中存在且可剔除的参数名列表。
// named 非空时只返回它（若存在）；否则返回 uncommon 清单与出站体的交集。
// coreParams 永不返回。
func StrippableParams(outbound []byte, named string) []string {
	obj := parseOutboundTopLevel(outbound)
	if obj == nil {
		return nil
	}
	if named != "" {
		for _, n := range strings.Split(named, ",") {
			n = strings.ToLower(strings.TrimSpace(n))
			if n == "" || coreParams[n] {
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
			if n == "" || coreParams[n] {
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
