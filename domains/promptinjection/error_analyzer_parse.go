package promptinjection

// error_analyzer_parse.go — LLM 分析输出的 JSON 层：保序解码 / 候选块 / 容错抢救。
//
// ★ 背景（2026-11-25，三台生产实测）：存量 15 条真实分析里 **14 条**被解析器
//
//	丢弃（root_cause 落成占位值 llm_parse_failed、置信度 0）。逐条看原始响应，
//	失败形态只有四类，没有一类是「模型没分析出来」：
//
//	① 合法 JSON，但字段散落在嵌套结构里（6 条）——模型按自己的报告体例返回：
//	   analysis_report.root_cause_analysis.primary_cause / diagnosis.root_cause /
//	   中文键 分析结果.根因分析.详细说明 / remediation.immediate_actions[]；
//	② 输出被截断（4 条）——推理模型把 token 花在思考上，正文在字符串中途断掉；
//	③ 字符串里有裸换行、或个别字段有语法错（2 条）——整篇因此不合法；
//	④ suggested_fix 是对象而不是字符串（211 实测）——类型不符，整条被丢。
//
//	⇒ 旧解析器 = 一次严格的结构体反序列化，任何一处不符 ⇒ 整条丢弃。
//	⇒ 本文件把「解码」拆成三层：严格 → 抢救 → 放弃，并**保序**解码
//	  （修复建议常是「立即措施/短期修复/长期优化」这样的有序分组，
//	  解成 map 会丢序 ⇒ 渲染出来的建议顺序每次都不同）。
//
// 字段的识别与类型宽 coercion 在 error_analyzer_harvest.go。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ── 保序解码 ───────────────────────────────────────────────────────────────

// ordObj 是保序的 JSON 对象（标准库解成 map 会丢键序）。
type ordObj struct {
	keys []string
	vals map[string]any
}

func (o *ordObj) get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// normIndex 返回「归一键 → 原始键」（同一归一形态先出现者胜）。
func (o *ordObj) normIndex() map[string]string {
	idx := make(map[string]string, len(o.keys))
	for _, k := range o.keys {
		nk := normKey(k)
		if _, dup := idx[nk]; !dup {
			idx[nk] = k
		}
	}
	return idx
}

// decodeOrderedValue 用 dec 读一个完整 JSON 值（对象保序、数字保字面）。
func decodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := &ordObj{vals: map[string]any{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := kt.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			v, err := decodeOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			if _, dup := obj.vals[key]; !dup {
				obj.keys = append(obj.keys, key)
			}
			obj.vals[key] = v
		}
		if _, err := dec.Token(); err != nil { // '}'
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := decodeOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil { // ']'
			return nil, err
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

func newOrderedDecoder(text string) *json.Decoder {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	return dec
}

// decodeStrictOrdered 严格解码：整段文本必须恰好是一个 JSON 对象。
func decodeStrictOrdered(text string) (*ordObj, error) {
	dec := newOrderedDecoder(text)
	v, err := decodeOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	// 整段必须刚好用完（尾部只许空白）。
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after JSON value")
	}
	obj, ok := v.(*ordObj)
	if !ok {
		return nil, errors.New("top-level value is not an object")
	}
	return obj, nil
}

// decodeOneValue 从 text 开头解出一个 JSON 值；尾部只许空白与收尾的 } ] ,
// （截断输出的最后一个成员后面可能跟着多层的收括号）。
func decodeOneValue(text string) (any, error) {
	dec := newOrderedDecoder(text)
	v, err := decodeOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	// ★ 必须用 InputOffset（流上的绝对偏移）取尾部：dec.Buffered() 只含
	//   解码器**内部缓冲**里没消费的那一段——输入超过一次填充（512 字节）时
	//   它拿不全尾部 ⇒ 「后面还有没有东西」会判错。
	rest := text[dec.InputOffset():]
	if strings.Trim(rest, " \t\r\n}],") != "" {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

// toPlain 把保序结构转回标准库形状（落库/序列化用）。
//
// 数字：带小数点/指数的字面 → float64；整数 → int64（与旧解析器的 float64
// 输出在 JSON 文本上等价）；json.Number 原样兜底。
func toPlain(v any) any {
	switch t := v.(type) {
	case *ordObj:
		out := make(map[string]any, len(t.keys))
		for _, k := range t.keys {
			out[k] = toPlain(t.vals[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = toPlain(item)
		}
		return out
	case json.Number:
		return numberValue(t)
	default:
		return v
	}
}

func numberValue(n json.Number) any {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	f, err := n.Float64()
	if err != nil {
		return s
	}
	return f
}

// ── 候选块 ─────────────────────────────────────────────────────────────────

// jsonCandidates 按出现顺序返回响应里所有可能的 JSON 文本块（去重）：
// ``` 围栏块 + 括号配平的顶层 {…} 块。
func jsonCandidates(response string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	// ① 围栏块（```json … ``` 或 ``` … ```）。
	rest := response
	for {
		i := strings.Index(rest, "```")
		if i < 0 {
			break
		}
		body := rest[i+3:]
		nl := strings.IndexByte(body, '\n')
		if nl >= 0 && !strings.ContainsAny(body[:nl], `{}`) {
			body = body[nl+1:] // 丢掉语言行
		}
		j := strings.Index(body, "```")
		if j < 0 {
			// 围栏没闭合（输出被截断）⇒ 取到结尾。
			add(body)
			break
		}
		add(body[:j])
		rest = body[j+3:]
	}

	// ② 括号配平块（字符串里的括号不计）。
	for _, block := range jsonObjectCandidates(response) {
		add(block)
	}

	// ③ 没闭合的块（输出在中途被截断）：从最外层未闭合的 { 起到结尾。
	if i := unclosedOpenBrace(response); i >= 0 {
		add(response[i:])
	}
	return out
}

// unclosedOpenBrace 返回最外层「没有配对闭合」的 { 的下标（没有 ⇒ -1）。
//
//	只数字符串字面量之外的括号。
func unclosedOpenBrace(text string) int {
	var stack []int
	inString := false
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, i)
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(stack) == 0 {
		return -1
	}
	return stack[0]
}

// ── 抢救 ───────────────────────────────────────────────────────────────────

var topLevelKeyLineRE = regexp.MustCompile(`(?m)^([ \t]*)"((?:[^"\\\r\n]|\\.)*)"[ \t]*:`)

// salvageResult 是一次抢救的读数。
type salvageResult struct {
	obj     *ordObj
	lost    []string // 没能解出来的顶层键
	cut     bool     // 文本在最后一个成员中途断掉（输出被截断）
	partial bool     // 至少有一个成员解出，但也有成员丢失
}

const maxSalvageDepth = 6

// salvageObject 在一段对象文本不合法时，把**成员逐个独立解码**（可递归）。
//
// ★ 为什么逐成员而不是「把 JSON 修合法」：
//
//	截断/语法错/类型不符都只毁掉**它所在的那个成员**；逐成员解码时
//	一个成员失败不影响其它成员 ⇒ 坏点前后两侧的完整字段都能救回来
//	（而「补全括号再整体解」对「中间某处语法错」无能为力）。
//
// ★ 嵌套同理：顶层成员 analysis: {…} 的内部有一处语法错时，这个顶层成员
//
//	整体解不出来——但根因往往就在它里面 ⇒ 对它的文本**递归**做同样的事
//	（丢掉坏的那个内层成员、保住其余），丢失的键以 analysis.risk_assessment
//	的路径形态如实登记。
//
// ★ 锚点 = 与首个键行**同缩进**的 `"key":` 行。pretty 输出里本层键的缩进
//
//	最浅、下层键更深 ⇒ 同缩即本层。缩进不规范时会误切 ⇒ 被切的成员解不出来、
//	按丢失处理（只会少救，不会救错：解出来的成员都是合法 JSON 值）。
//	没有行锚点（单行压缩输出）⇒ 退化为顺序流式解码：解到坏点为止。
func salvageObject(text string, depth int) salvageResult {
	locs := topLevelKeyLineRE.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return salvageSequential(text, depth)
	}
	indent := text[locs[0][2]:locs[0][3]]
	var res salvageResult
	obj := &ordObj{vals: map[string]any{}}
	cut := false
	partial := false
	for i, loc := range locs {
		if text[loc[2]:loc[3]] != indent {
			continue
		}
		key := unescapeJSONString(text[loc[4]:loc[5]])
		end := len(text)
		if next := nextSameIndent(locs, i+1, indent, text); next >= 0 {
			end = next
		}
		seg := strings.TrimLeft(text[loc[1]:end], " \t")
		v, err := decodeOneValue(seg)
		if err != nil {
			// 内层对象自己有坏点 ⇒ 递归抢救它。
			if strings.HasPrefix(seg, "{") {
				if sub := salvageObject(seg, depth+1); sub.obj != nil {
					for _, lk := range sub.lost {
						res.lost = append(res.lost, key+"."+lk)
					}
					v, err, partial = sub.obj, nil, true
					if sub.cut {
						cut = true
					}
				}
			}
			if err != nil {
				res.lost = append(res.lost, key)
				if end == len(text) {
					cut = true
				}
				continue
			}
		}
		if _, dup := obj.vals[key]; !dup {
			obj.keys = append(obj.keys, key)
		}
		obj.vals[key] = v
	}
	if len(obj.keys) == 0 {
		return salvageResult{lost: res.lost, cut: cut}
	}
	res.obj = obj
	res.cut = cut
	res.partial = partial || len(res.lost) > 0
	return res
}

func nextSameIndent(locs [][]int, from int, indent, text string) int {
	for j := from; j < len(locs); j++ {
		if text[locs[j][2]:locs[j][3]] == indent {
			return locs[j][0]
		}
	}
	return -1
}

// salvageSequential 是没有行锚点时的顺序流式解码（解到第一个坏点为止）。
func salvageSequential(text string, depth int) salvageResult {
	if depth > maxSalvageDepth {
		return salvageResult{cut: true}
	}
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return salvageResult{}
	}
	dec := newOrderedDecoder(text[start:])
	if _, err := dec.Token(); err != nil { // '{'
		return salvageResult{cut: true}
	}
	obj := &ordObj{vals: map[string]any{}}
	cut := false
	var lost []string
	var lastKey string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			// 流在两个成员之间断掉（缺逗号/被截断）：不知道后面的键名 ⇒ 用
			// 「最后一个读出的键 + …」登记断点，不假装什么都没丢。
			if lastKey != "" {
				lost = append(lost, lastKey+"…")
			}
			cut = true
			break
		}
		key, ok := kt.(string)
		if !ok {
			cut = true
			break
		}
		v, err := decodeOrderedValue(dec)
		if err != nil {
			lost = append(lost, key)
			cut = true
			break
		}
		lastKey = key
		if _, dup := obj.vals[key]; !dup {
			obj.keys = append(obj.keys, key)
		}
		obj.vals[key] = v
	}
	if len(obj.keys) == 0 {
		return salvageResult{lost: lost, cut: cut}
	}
	return salvageResult{obj: obj, lost: lost, cut: cut, partial: len(lost) > 0}
}

// cleanResponse 做与语法无关的文本清洗：去 BOM/零宽字符、修非法 UTF-8、
// 统一换行。纯文本行不受影响。
func cleanResponse(response string) string {
	s := strings.TrimPrefix(response, "\uFEFF")
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch r {
		case 0x200B, 0x200C, 0x200D, 0xFEFF: // 零宽字符
			return -1
		}
		return r
	}, s)
	return s
}

// escapeRawControlChars 把**字符串字面量内部**的裸控制字符（换行/制表等）
// 转成合法转义。字符串外的空白（结构性换行）原样保留。
//
// ★ 这是「字符串里有裸换行」这类不合法输出的修法（245 实测形态 ③）。
// 只在严格解码失败之后才用（合法输出不会走到这里）。
func escapeRawControlChars(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 32)
	inString := false
	escaped := false
	for _, r := range text {
		if !inString {
			if r == '"' {
				inString = true
			}
			b.WriteRune(r)
			continue
		}
		if escaped {
			escaped = false
			b.WriteRune(r)
			continue
		}
		switch {
		case r == '\\':
			escaped = true
			b.WriteRune(r)
		case r == '"':
			inString = false
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stripTrailingCommas 去掉 } ] 前面的尾随逗号。
func stripTrailingCommas(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	for i, r := range runes {
		if r == ',' {
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			if j < len(runes) && (runes[j] == '}' || runes[j] == ']') {
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unescapeJSONString 把 JSON 字面量里的键还原成普通文本（只用于诊断信息）。
func unescapeJSONString(s string) string {
	var out string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &out); err != nil {
		return s
	}
	return out
}

// proseText 取响应里的自由文本（去围栏、去 JSON 骨架）——自由文本兜底用。
func proseText(response string) string {
	s := cleanResponse(response)
	if i := strings.Index(s, "```"); i >= 0 {
		s = strings.ReplaceAll(s, "```json", "\n")
		s = strings.ReplaceAll(s, "```", "\n")
	}
	// 去掉 JSON 行骨架（"key": "value" → value），保留值文本。
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "{" || line == "}" || line == "```" {
			continue
		}
		if strings.HasPrefix(line, `"`) {
			// "key": "value",  /  "key": {  /  "key": [
			if k, v, ok := splitJSONMember(line); ok {
				if v != "" {
					out = append(out, strings.Trim(v, `",`))
				} else if k != "" {
					out = append(out, "【"+strings.Trim(k, `" `)+"】")
				}
				continue
			}
			out = append(out, strings.Trim(line, `",`))
			continue
		}
		out = append(out, strings.Trim(line, ","))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func splitJSONMember(line string) (key, value string, ok bool) {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:colon])
	value = strings.TrimSpace(line[colon+1:])
	return key, strings.Trim(value, `{}[], `), true
}

// runeTruncate 按 rune 截断（不切坏多字节字符）。
func runeTruncate(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	var b bytes.Buffer
	count := 0
	for _, r := range s {
		if count == n {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String() + "…"
}

// ── 截断修复 ───────────────────────────────────────────────────────────────

// repairTruncatedJSON 把「在半路被掐断」的 JSON 文本补成语法完整的文档。
//
//	返回 (补好的文本, 被丢掉的成员, 是否做了修复)。文本没有未闭合的括号 ⇒ ok=false。
//
//	★ 为什么需要它（2026-11-26 实测）：输出额度用完时，最常见的断点是**某个字符串
//	  的中间**（模型写到半句话就停了）。逐成员打捞在这种断点上无能为力——打捞的
//	  切分锚点是「与首个键行同缩进的键行」，而未闭合的引号会让后面的一切都成了
//	  「字符串的一部分」⇒ 一个成员都切不出来 ⇒ 整条回答被当成散文 ⇒ 连前面已经
//	  完整写出的根因、分类、建议都丢了，而且不标记截断。
//	★ 做法：丢掉**没写完的那个成员**（它的值本来就不完整），补齐所有未闭合的括号，
//	  再交给严格解码——解出来的每个成员都是模型完整写出的。
//	★ 被丢掉的成员以键名登记（键名没写出来时记 truncated-member），调用方据此
//	  标记「输出被截断」⇒ 置信度封顶、不进自动应用。
func repairTruncatedJSON(text string) (string, []string, bool) {
	var (
		stack           []truncFrame // 未闭合的 { [（连同它在父对象里的键名）
		inString        bool
		escaped         bool
		lastDelim       = -1 // 字符串之外最近的 { [ , 的下标
		lastColon       = -1 // 字符串之外最近的 : 的下标
		pendingKey      string
		lastStringStart = -1
		lastStringEnd   = -1
	)
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
				lastStringEnd = i
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			lastStringStart = i
		case '{', '[':
			stack = append(stack, truncFrame{open: c, key: pendingKey})
			lastDelim = i
			pendingKey = ""
		case '}', ']':
			if len(stack) == 0 || (stack[len(stack)-1].open == '{') != (c == '}') {
				return "", nil, false // 括号错配：不是截断，交给别的路径
			}
			stack = stack[:len(stack)-1]
		case ',':
			lastDelim = i
			pendingKey = ""
		case ':':
			lastColon = i
			// 紧挨着冒号前面的那个字符串是键。
			if lastStringEnd >= 0 && strings.TrimSpace(text[lastStringEnd+1:i]) == "" && lastStringStart >= 0 {
				pendingKey = strings.Trim(unescapeJSONString(text[lastStringStart:lastStringEnd+1]), `"`)
			}
		}
	}
	if len(stack) == 0 {
		return "", nil, false // 没有未闭合的括号 ⇒ 不是被截断的
	}

	cut := len(text)
	var lost []string
	// 丢失成员按**路径**登记（例：analysis_details.patch.diff），与逐成员打捞同一口径。
	path := make([]string, 0, len(stack)+1)
	for _, f := range stack {
		if f.key != "" {
			path = append(path, f.key)
		}
	}
	name := strings.Join(append(path, pendingKey), ".")
	if strings.HasSuffix(name, ".") {
		name = strings.TrimSuffix(name, ".") + ".truncated-member"
	}
	if inString {
		// 停在字符串中间：这个成员没写完 ⇒ 整个丢掉，退到最近的分隔符之后。
		cut = lastDelim + 1
		lost = append(lost, name)
	} else {
		// 不在字符串里：看最后一个值是不是写完了的完整 JSON 值。
		from := lastDelim
		if lastColon > from {
			from = lastColon
		}
		tail := strings.TrimSpace(text[from+1:])
		if tail != "" && !json.Valid([]byte(tail)) {
			// 数字 / 关键字写到一半（例：0. / tru / nul）⇒ 同样丢掉这个成员。
			cut = lastDelim + 1
			lost = append(lost, name)
		}
	}

	out := text[:cut]
	for len(stack) > 0 {
		out = strings.TrimRight(out, " \t\r\n")
		// 先补右括号、再去它前面的尾随逗号：stripTrailingCommas 只认「逗号后紧跟 } ]」。
		// 从里往外逐层补 ⇒ 内层对象先闭合。
		if stack[len(stack)-1].open == '{' {
			out = stripTrailingCommas(out + "}")
		} else {
			out = stripTrailingCommas(out + "]")
		}
		stack = stack[:len(stack)-1]
	}
	return out, lost, true
}

// truncFrame 是截断修复扫描时的一个未闭合容器：左括号 + 它在父对象里的键名。
type truncFrame struct {
	open byte
	key  string
}
