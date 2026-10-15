package reqprobe

// optional.go — P0-B（2026-10-14）：从 internal/paramrefusal 收敛过来的
// 「可选字段白名单 / 黑名单 / 线体键派生」与「拒绝即学习」判定。
//
// 与 StripParams 骨架集的关系：
//   - Strippable / StripCandidates 回答「这个字段族可不可以进剔除记忆」；
//   - isSkeleton(key, wire) 回答「在该线格式上点名了能不能真剥」；
//   - LearnOnRefusal 要求两者同时成立：白名单 + 非骨架（按线）。

import "strings"

// coreNeverStrip 是永不进「拒绝即学习」白名单的核心字段。
var coreNeverStrip = map[string]bool{
	"model":       true,
	"messages":    true,
	"tools":       true,
	"tool_choice": true,
	"stream":      true,
	"max_tokens":  true,
	"temperature": true,
	"top_p":       true,
	"stop":        true,
	"n":           true,
	"user":        true,
	"input":       true,
	"contents":    true,
}

// optionalLearnFamily 是拒绝即学习的可选字段族（magpie optionalFields 对齐 +
// 本仓 r1020/r1021/r1026 实证）。stream_options 在列——但是否真剥仍看
// isSkeleton(wire)：chat 线学了也不剥，responses 线又学又剥。
var optionalLearnFamily = map[string]bool{
	"stream_options":         true,
	"thinking":               true,
	"reasoning_effort":       true,
	"reasoning":              true,
	"metadata":               true,
	"parallel_tool_calls":    true,
	"service_tier":           true,
	"logprobs":               true,
	"top_logprobs":           true,
	"web_search_options":     true,
	"prediction":             true,
	"store":                  true,
	"prompt_cache_key":       true,
	"prompt_cache_retention": true,
	"safety_identifier":      true,
	"verbosity":              true,
	"enable_thinking":        true,
}

// Strippable 报告字段是否属于可剔除可选族（黑名单优先，未登记 fail-closed）。
func Strippable(field string) bool {
	f := strings.ToLower(strings.TrimSpace(field))
	if !validOptionalFieldName(f) {
		return false
	}
	if coreNeverStrip[f] {
		return false
	}
	return optionalLearnFamily[f]
}

// StripCandidates 从提取层字段名派生候选顶层线体键（只保留 Strippable）。
func StripCandidates(field string) []string {
	raw := strings.TrimSpace(strings.Trim(strings.TrimSpace(field), "'\"`"))
	if raw == "" {
		return nil
	}
	seg := strings.Split(raw, ".")
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || !Strippable(s) {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(seg) > 1 && strings.EqualFold(seg[0], "body") {
		add(seg[len(seg)-1])
		add(strings.Join(seg[1:], "."))
		return out
	}
	add(raw)
	add(seg[0])
	if len(seg) > 1 {
		add(seg[len(seg)-1])
	}
	return out
}

// LearnOnRefusal 判定「拒绝即学习」：字段在可选族内，且该线格式上不是骨架。
func LearnOnRefusal(field, wire string) bool {
	f := strings.ToLower(strings.TrimSpace(field))
	if !Strippable(f) {
		return false
	}
	return !isSkeleton(f, wire)
}

func validOptionalFieldName(f string) bool {
	if len(f) == 0 || len(f) > 64 {
		return false
	}
	for i := 0; i < len(f); i++ {
		c := f[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '-':
		default:
			return false
		}
	}
	return true
}
