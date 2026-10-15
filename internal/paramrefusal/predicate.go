package paramrefusal

// predicate.go — P0-1 白名单/黑名单谓词与线体键派生。
//
// 黑名单优先于一切：核心字段（请求成形骨架）被上游点名也绝不剔除——
// 「上游拒了 messages」说明是别的问题（模型/路由/上游 bug），剔除只会把
// 请求打成必败形态。
//
// paramreg 说明：FieldSpec 的 Kind 语义是「扩展字段的还原策略」
// （IRHandled/Portable/DialectOnly/Translatable），**没有 optional/required
// 语义可查**——无法按注册表查询「哪些字段可选」。因此按 P0-1 方案静态
// 维护 strippableFamily 表。维护方式：新增可剔字段的判据 = 上游 400 实证
// 指名 + 剔除后请求语义不损（模型侧默认值兜底）；登记时在 Note 注明来源
// 事故。reqprobe 的 uncommonParams（无名兜底清单）是另一维护线的参考。

import "strings"

// coreNeverStrip 是永不剔除的核心字段（P0-1 硬黑名单）。
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
	"contents":    true, // Gemini 线格式的消息载体
}

// strippableFamily 是显式可选字段家族（P0-1 白名单）。全部有「上游 400
// 指名 + 剔除不损请求成形」的实证（raw-logs 2026-10-13 vapeur 三案 +
// r1020/r1021/r1026 系列）。
var strippableFamily = map[string]bool{
	"stream_options":      true, // r1026/r1026b：Responses 线格式无此字段
	"thinking":            true, // r1021：thinking 对象对严格中转硬 400
	"reasoning_effort":    true,
	"reasoning":           true,
	"metadata":            true,
	"parallel_tool_calls": true,
	"service_tier":        true,
	"logprobs":            true,
	"top_logprobs":        true,
	"web_search_options":  true,
	"prediction":          true,
}

// Strippable 报告一个字段是否可剔除：黑名单永不剔；白名单（家族表）才可剔；
// 未登记字段（含点号路径、畸形名、超长名）一律 false（fail-closed——宁可
// 不剔也不要误删客户端语义）。
func Strippable(field string) bool {
	f := strings.ToLower(strings.TrimSpace(field))
	if !validFieldName(f) {
		return false
	}
	if coreNeverStrip[f] {
		return false
	}
	return strippableFamily[f]
}

// StripCandidates 从提取层拿到的（可能带点号路径的）字段名派生候选顶层
// 线体键，只保留 Strippable 的：
//
//   - vapeur 点名嵌套路径 'stream_options.include_usage' → 顶层键是首段
//     stream_options（首段/末段/全名三个候选交给在场判断——StripTopLevel
//     JSONFields 只删真实存在的顶层键，多余候选无害）；
//   - pydantic loc 命名空间 ["body","x"] → 真键是 x（body 段剥掉）。
//
// 返回已去重、保持推导顺序的列表。
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
		// body.x → x；body.a.b → 末段 b（pydantic loc 尾元素是真键）。
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

// validFieldName：非空、≤64、[A-Za-z0-9_.-]（与提取层 sanitize 同一约束，
// 这里是记忆侧的第二道门）。
func validFieldName(f string) bool {
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
