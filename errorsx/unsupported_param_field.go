package errorsx

// unsupported_param_field.go — P0-1（2026-10-14，
// docs/research/2026-10-14-magpie对比与优化方案.md）：从上游 400/422 错误体
// 中提取被指名拒绝的**可选参数名**，供 paramrefusal（internal/paramrefusal）
// 做「剔除→重试一次→记忆」。与 classify.go 的 unsupportedParamRe 的分工：
// 那条正则只回答「这类 4xx 是不是参数被拒」（ErrorKind 分类用），本文件
// 回答「具体是哪个字段」。词表族与 magpie 的 refusedOptional 对齐：
//
//	"Unknown parameter: 'stream_options'."            ← Vapeur/raw-logs 2026-10-13
//	"Unsupported parameter: 'temperature' is not ..."  ← Vapeur gpt-5.6-sol
//	"Unrecognized request argument supplied: x"        ← OpenAI
//	"Extra inputs are not permitted (name 'x')"        ← pydantic 文本形态
//	{"detail":[{"type":"extra_forbidden","loc":["body","x"], ...}]} ← pydantic JSON
//	"Unsupported ... field 'x'"                         ← 中转泛化形态
//
// 纯文本提取、不看 HTTP 状态（状态门在调用方 executor 钩子上：400/422）。
// 不改变任何既有 ErrorKind 字符串值（DB 持久化契约）。

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf16"
)

// maxRefusalBodyLen 限制参与提取的错误体长度：4xx 错误体远小于此值，
// 防御性封顶避免畸形超大 body 拖慢热路径。
const maxRefusalBodyLen = 32 << 10

// refusedParamTextPatterns 按文本匹配「指名拒绝参数」的词表族。
// 每条捕获组 1 = 字段名（可能带点号路径，由 paramrefusal.StripCandidates
// 再派生顶层线体键；这里只负责把名字提出来）。
//
// 提取纪律（2026-10-13 r1026b 审计的教训在提取层的翻版）：**名字必须被
// 定界符完整包住**。带前引号的名字必须以后引号闭合（'a\nb' 这类畸形名
// 不得截断成 "a"）；裸名形态必须以非名字字符收界（65 字符超长名不得静默
// 截成前 64 个）。否则畸形提取会以「合法字段名」的身份进入剔除/记忆链。
var refusedParamTextPatterns = buildRefusedParamPatterns()

func buildRefusedParamPatterns() []*regexp.Regexp {
	// 家族前缀：后面直接跟名字。
	families := []string{
		// "Unknown parameter: 'stream_options'." / "unsupported parameter: xxx"
		// / "Validation: Unsupported parameter(s): `web_search`"（kimi 形态）。
		`(?i)(?:unknown|unsupported|unrecognized|unexpected)[ _-]?parameters?(?:\([a-z]+\))?\s*[:：]\s*`,
		// "Unrecognized request argument supplied: reasoning_effort"（OpenAI）。
		`(?i)unrecognized request arguments?\s*(?:supplied)?\s*[:：]?\s*`,
	}
	const (
		quotedName = `['"\x60]([A-Za-z_][A-Za-z0-9_.\-]{0,63})['"\x60]`
		// 裸名形态的收界：名字后必须是非名字字符（捕获组 2）或串尾。
		// RE2 无 lookahead，用边界捕获组达成同等校验——超长名（>64）不会
		// 被静默截成前 64 个（所有切分点后都是名字字符 → 整体不匹配）。
		bareName = `([A-Za-z_][A-Za-z0-9_.\-]{0,63})([^A-Za-z0-9_.\-]|$)`
	)
	var out []*regexp.Regexp
	for _, fam := range families {
		// 带引号形态优先（定界明确，无截断风险）。
		out = append(out, regexp.MustCompile(fam+quotedName))
		// 裸名形态（无引号家族文案）。
		out = append(out, regexp.MustCompile(fam+bareName))
	}
	out = append(out,
		// pydantic 文本形态："Extra inputs are not permitted (name 'account_id')"。
		regexp.MustCompile(`(?i)extra inputs are not permitted\s*\(name\s*['"\x60]([A-Za-z_][A-Za-z0-9_.\-]{0,63})['"\x60]\)`),
		// "Unsupported ... field 'x'"（中转泛化：Unsupported request field /
		// Unsupported extra field 等）。
		regexp.MustCompile(`(?i)unsupported\s+[a-z ]{0,32}?field\s+['"\x60]([A-Za-z_][A-Za-z0-9_.\-]{0,63})['"\x60]`),
		// pydantic JSON 的裸文本兜底（结构化解析失败/被截断时）：
		// "code":"extra_forbidden" ... "loc":["body","x"]，两个键序都覆盖。
		regexp.MustCompile(`(?i)"code"\s*:\s*"extra_forbidden"[\s\S]{0,240}?"loc"\s*:\s*\[[^\]]*?"([A-Za-z_][A-Za-z0-9_.\-]{0,63})"`),
		regexp.MustCompile(`(?i)"loc"\s*:\s*\[[^\]]*?"([A-Za-z_][A-Za-z0-9_.\-]{0,63})"[\s\S]{0,240}?"code"\s*:\s*"extra_forbidden"`),
	)
	return out
}

// RefusedOptionalParam 从上游 400/422 错误体提取被指名拒绝的参数名。
//
// 返回的字段名是「纯名字」：已剥引号/反引号；pydantic loc 的 body 命名空间
// 已剥（["body","x"] → x）；只接受 [A-Za-z0-9_.-] 且总长 ≤64（防注入式
// 畸形提取——路径穿越/控制字符一律提不出来）。上游可能指名嵌套路径
// （'stream_options.include_usage'），此时返回完整点号名，由调用方派生
// 顶层线体键。ok=false 表示错误体没有可辨识的指名拒绝。
func RefusedOptionalParam(body []byte) (string, bool) {
	if len(body) == 0 || len(body) > maxRefusalBodyLen {
		return "", false
	}
	// decodeRefusalUnicodeEscapes：上游 400 体常带 \uXXXX 转义（\u0027 = 引号），
	// 不解码则词表正则全部断链（raw-logs 2026-10-13 的 vapeur 样本即该
	// 形态）。与 reset_time_parser.go 的 decodeResetUnicodeEscapes 同族
	// 但互不共享（P0-4 会话刻意分名，避免共享私有 helper 的跨文件耦合）。
	decoded := decodeRefusalUnicodeEscapes(body)
	// 1) 结构化优先：pydantic 的 extra_forbidden + loc 数组（JSON 游走，
	//    对键序/嵌套鲁棒）。
	if field, ok := refusedParamFromStructuredJSON(decoded); ok {
		return field, true
	}
	// 2) 文本词表族。
	text := string(decoded)
	for _, re := range refusedParamTextPatterns {
		if m := re.FindStringSubmatch(text); m != nil {
			if field, ok := sanitizeRefusedFieldName(m[1]); ok {
				return field, true
			}
		}
	}
	return "", false
}

// refusedParamFromStructuredJSON 游走 JSON（深度封顶）寻找 pydantic v2 的
// extra_forbidden 错误项，取 loc 数组的最后一个字符串元素（["body","x"]
// → "x"；["body","a","b"] → "b"）。找不到返回 false。
func refusedParamFromStructuredJSON(body []byte) (string, bool) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false
	}
	found := ""
	var walk func(node any, depth int)
	walk = func(node any, depth int) {
		if depth > 5 || found != "" {
			return
		}
		switch v := node.(type) {
		case map[string]any:
			code, hasCode := stringField(v, "code", "type")
			if hasCode && strings.EqualFold(code, "extra_forbidden") {
				if loc, ok := v["loc"].([]any); ok {
					last := ""
					for _, el := range loc {
						if s, ok := el.(string); ok {
							last = s
						}
					}
					if last != "" {
						if f, ok := sanitizeRefusedFieldName(last); ok {
							found = f
							return
						}
					}
				}
			}
			for _, child := range v {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range v {
				walk(child, depth+1)
			}
		}
	}
	walk(parsed, 0)
	if found != "" {
		return found, true
	}
	return "", false
}

func stringField(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// sanitizeRefusedFieldName 清洗提取结果：剥引号/反引号/空白；pydantic 的
// body 命名空间前缀剥掉（"body.x" → "x"）；字符集与长度校验（≤64，
// [A-Za-z0-9_.-]）。任何不合规都拒绝（ok=false）。
func sanitizeRefusedFieldName(raw string) (string, bool) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "'\"\x60"))
	s = strings.TrimPrefix(s, "body.")
	if s == "" || len(s) > 64 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '-':
		default:
			return "", false
		}
	}
	return s, true
}

// decodeRefusalUnicodeEscapes 把 body 里的 \uXXXX 转义解码成真实字节（UTF-8）。
// 非法转义原样保留；代理对（surrogate pair）正确合并。纯 ASCII body
// 快速路径直接返回，零分配。
func decodeRefusalUnicodeEscapes(body []byte) []byte {
	hasEscape := false
	for i := 0; i+1 < len(body); i++ {
		if body[i] == '\\' && body[i+1] == 'u' {
			hasEscape = true
			break
		}
	}
	if !hasEscape {
		return body
	}
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); {
		if body[i] == '\\' && i+5 < len(body) && body[i+1] == 'u' {
			if r, ok := parseRefusalHex4(body[i+2 : i+6]); ok {
				i += 6
				// 代理对：\uD83D\uDE00 → 单个 rune。
				if utf16.IsSurrogate(r) && i+6 <= len(body) && body[i] == '\\' && body[i+1] == 'u' {
					if r2, ok2 := parseRefusalHex4(body[i+2 : i+6]); ok2 {
						if combined := utf16.DecodeRune(r, r2); combined != 0xFFFD {
							r = combined
							i += 6
						}
					}
				}
				if utf16.IsSurrogate(r) {
					r = 0xFFFD
				}
				b.WriteRune(r)
				continue
			}
		}
		b.WriteByte(body[i])
		i++
	}
	return []byte(b.String())
}

func parseRefusalHex4(s []byte) (rune, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var v rune
	for _, c := range s {
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}
