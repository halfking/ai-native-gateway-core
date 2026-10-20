package sanitize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Reserve every literal marker before processing any field. This also handles
// JSON unicode escapes and nested tool JSON strings, so allocation order cannot
// make a client-supplied marker alias a newly allocated credential.
func reserveInputPlaceholders(body []byte, offsets map[SensitiveType]int) error {
	return reserveInputPlaceholdersDepth(body, offsets, 0)
}

func reserveInputPlaceholdersDepth(body []byte, offsets map[SensitiveType]int, depth int) error {
	if depth > maxNativeToolInputDepth {
		return fmt.Errorf("%w: embedded JSON depth", errInvalidSanitizeInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: placeholder scan", errInvalidSanitizeInput)
		}
		if text, ok := token.(string); ok {
			document := bytes.TrimSpace([]byte(text))
			if len(document) > 0 && (document[0] == '{' || document[0] == '[') && json.Valid(document) {
				if err := reserveInputPlaceholdersDepth(document, offsets, depth+1); err != nil {
					return err
				}
			}
			for _, marker := range PlaceholderPattern.FindAllString(text, -1) {
				if p, ok := ParsePlaceholder(marker); ok && p.Index > offsets[p.Type] {
					offsets[p.Type] = p.Index
				}
			}
			reserveMutatedPlaceholders(text, offsets)
		}
	}
}

func sortedRawKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func credentialField(key string) bool {
	return secretCredentialField(key) || identityField(key)
}

// secretCredentialField 报告 key 是否「密码族」凭据字段——值本身是机密
// （password/token/api_key 等）。输出侧防外传守卫对这类字段的明文值保持
// 硬阻断（r1020 契约：模型写出的明文凭据整条拦）。
func secretCredentialField(key string) bool {
	switch strings.ToLower(key) {
	case "password", "passwd", "pwd", "secret", "token", "api_key", "apikey", "access_key", "authorization", "密码", "口令":
		return true
	}
	return false
}

// identityField 报告 key 是否「身份族」字段——值是登录身份（用户名/账号），
// 不是机密本身。r1024（2026-10-13，minimax-m3 多轮 tool E2E）：
// 输出守卫原来按 credentialField 一律硬阻断非占位符值，而裸用户名
// （"deploy"/"root"/"ops"）不命中任何输入检测规则、永远不可能以占位符
// 形态出现，于是「带 username 的 SSH 类工具调用」被结构性永久掐断
// （客户端零内容）。身份不是凭据：把它从阻断集合里拆出去，仍走
// 检测器 + 外传目的地检查。输入侧占位符分配（TypeAccount）不受影响。
func identityField(key string) bool {
	switch strings.ToLower(key) {
	case "account", "username", "user_name", "login", "帐号", "账号", "账户", "用户名":
		return true
	}
	return false
}

// placeholderCredentialValues 是「模型自造的非敏感占位值」字面量集（整值、
// 大小写不敏感、两侧空白容忍）。
//
// r1024b（2026-10-13，minimax-m3 audit 场景生产复现）：模型在还没拿到部署
// 配置时会并行发出「先用占位值顶着」的工具调用（"password":"PLACEHOLDER"）。
// 这类值按定义不是机密——不是从会话里泄漏的真值，拦它只会把同轮里合法的
// 其它工具调用一并陪葬（客户端零内容）。封闭集方向是「豁免确定无害的拼写」，
// 任何未收录值保持 fail-closed 硬拦，与 r1020 的形态枚举禁区（把锚定当充分
// 条件）相反：这里枚举的是「定义上不构成泄漏」的字面量，不是「长得像无害」
// 的形状。
var placeholderCredentialValues = map[string]bool{
	"placeholder":   true,
	"<placeholder>": true,
	"changeme":      true,
	"todo":          true,
	"tbd":           true,
	"xxx":           true,
	"dummy":         true,
	"example":       true,
	"n/a":           true,
}

// credentialKeywordFamily 是凭据字段的关键词族（小写），用于「值自指涉字段名」
// 判定：USE_DEPLOY_PASSWORD / PASSWORD_HERE / <password> / ${password} 这类
// 值引用的是字段本身，是占位拼写而不是机密。模型每次自造的拼写都不同
// （实测 PLACEHOLDER 与 USE_DEPLOY_PASSWORD 并存），字面量集挡不住自造词，
// 自指涉判据才跟得上。
//
// 已知边界（如实登记）：真实密码里恰好含这些关键词（如 "Password1!"）会被
// 判为占位而放行。该值仍只回给会话属主（客户端），不构成对第三方的泄漏；
// 触网外传由目的地检查（hasPublicHostDestination / detectEgress）另行把守。
var credentialKeywordFamily = []string{
	"password", "passwd", "pwd", "secret", "token", "api_key", "apikey",
	"access_key", "authorization", "credential", "帐号", "账号", "密码", "口令",
}

// dummyCredentialValues 是「一眼假的示例值」字面量集（整值、大小写不敏感）。
//
// r1039（D7，2026-10-20）：`{"token":"abc123"}` / `{"password":"abc123"}`
// 之前被 `builtin.secret_field.egress` 整条拦下——可它们是**占位值**，
// 不是泄漏的真值。拦它的代价由每个请求承担（模型并行发起的其它工具调用
// 一起陪葬），而 owner 的口径是「不要放大范围，导致正常的工具执行失败」。
//
// 方向与 placeholderCredentialValues 一致：枚举的是「定义上不构成泄漏」的
// 拼写，不是「长得像无害」的形状。
var dummyCredentialValues = map[string]bool{
	"abc123": true, "abc1234": true, "abcdef": true,
	"aaaaaa": true, "000000": true, "0000": true,
	"redacted": true, "masked": true, "hidden": true,
	"notasecret": true, "not-a-secret": true, "notarealsecret": true,
	"your-token-here": true, "your-secret-here": true, "your-password-here": true,
	"your-token": true, "your_api_key_here": true,
	"fake": true, "sample": true, "test": true, "none": true, "null": true,
	"secret": true, "token": true, "password": true,
	// 纯数字串（123456 / 12345678）**不在**这一族：它们是现实里最常见的弱口令
	// 形态，r1024 的既有契约是「凭据字段上的普通明文照拦」。示例值判定只覆盖
	// 「字母 + 平凡数字」与掩码形态（见 isDummyCredentialValue）。
}

// dummyCredentialMaskPattern 匹配「被遮蔽过的值」形态：一串掩码字符
// （* / x / X / • / · / █），可带尾随数字（部分客户端展示「前 4 后 4」）。
//
// 例：***、xxxx、XXXXXX、****1234、••••。
var dummyCredentialMaskPattern = regexp.MustCompile(`^[xX*•·█]{2,}[0-9]*$`)

// dummyCredentialAlphaDigits 匹配「字母 + 数字」的示例值形态（字母 ≤ 6 位，
// 数字 3~4 位）。数字段是否「平凡」由 isTrivialDigitRun 判。
//
// 例：abc123、pass123、user0000、admin1234。
var dummyCredentialAlphaDigits = regexp.MustCompile(`^([A-Za-z]{1,6})([0-9]{3,4})$`)

// dummyCredentialMaxLength 是「示例值」判定的长度上限。
//
// r1039（D7）：owner 给的边界是「高熵、字母数字混合、长度 ≥ 16 的仍要拦」。
// ⇒ 16 位以上一律不按示例值处理。
const dummyCredentialMaxLength = 16

// isDummyCredentialValue 报告凭据字段的值是否「一眼假的示例值」。
//
// 判据分三族（都是**定义上不构成泄漏**的形态，不是「看起来弱」的强度判断）：
//
//  1. 字面量集（dummyCredentialValues）。
//  2. 掩码形态：*** / xxxx / ****1234。
//  3. 字母 + 平凡数字序列：abc123 / pass123 / user0000。
//
// 明确不覆盖的：
//
//   - 「短但随机」的值（hunter2、p@ss）——短不代表假，拦着。
//   - 长度 ≥ 16 的高熵值——那正是真密钥的形状，拦着。
//   - 含符号的混合值（R7#kQ?92!）——拦着。
func isDummyCredentialValue(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" || len(v) >= dummyCredentialMaxLength {
		return false
	}
	if dummyCredentialValues[strings.ToLower(v)] {
		return true
	}
	if dummyCredentialMaskPattern.MatchString(v) {
		return true
	}
	if m := dummyCredentialAlphaDigits.FindStringSubmatch(v); m != nil {
		return isTrivialDigitRun(m[2])
	}
	return false
}

// isTrivialDigitRun 报告数字段是否「平凡」：全同（000 / 111）或连续递增
// （123 / 2345）。
//
// 为什么要单独判：字母+数字的形态本身不足以说明是示例值——`hunter2` 也是
// 「字母+数字」，但它是货真价实的弱口令，不该放行。数字段的平凡性才是
// 「这是随手编的示例」的信号。
func isTrivialDigitRun(digits string) bool {
	if digits == "" {
		return false
	}
	allSame := true
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return true
	}
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[i-1]+1 {
			return false
		}
	}
	return true
}

// isPlaceholderCredentialValue 报告凭据字段的值是否「模型自造的占位拼写」。
// label 是该值所属的 JSON 键（凭据字段名），用于自指涉判定。
func isPlaceholderCredentialValue(label, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return true
	}
	if placeholderCredentialValues[v] {
		return true
	}
	// r1039（D7）：一眼假的示例值（abc123 / *** / user0000 …）同样是占位拼写。
	if isDummyCredentialValue(value) {
		return true
	}
	// 自指涉：值里包含凭据关键词（字段名或其同族词）。
	for _, kw := range credentialKeywordFamily {
		if strings.Contains(v, kw) {
			return true
		}
	}
	_ = label
	// 整体模板/占位包裹形态：<...>、${...}、$name、{name}、%s、{{...}}。
	if len(v) >= 2 && v[0] == '<' && v[len(v)-1] == '>' {
		return true
	}
	if strings.HasPrefix(v, "$") || strings.HasPrefix(v, "%") {
		return true
	}
	if len(v) >= 2 && v[0] == '{' && v[len(v)-1] == '}' {
		return true
	}
	return false
}

// Only call arguments/input are rewritten; names, IDs and schemas are opaque.
func (s *requestInputSanitizer) sanitizeToolCalls(raw json.RawMessage) (json.RawMessage, bool, error) {
	return s.sanitizeToolCallsDepth(raw, 0)
}

func (s *requestInputSanitizer) sanitizeToolCallsDepth(raw json.RawMessage, depth int) (json.RawMessage, bool, error) {
	if depth > maxNativeToolInputDepth {
		return nil, false, fmt.Errorf("%w: tool call depth", errInvalidSanitizeInput)
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		var calls []json.RawMessage
		if err := json.Unmarshal(raw, &calls); err != nil {
			return nil, false, fmt.Errorf("%w: tool calls", errInvalidSanitizeInput)
		}
		changed := false
		for i, call := range calls {
			updated, didChange, err := s.sanitizeToolCallsDepth(call, depth+1)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				calls[i], changed = updated, true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(calls)
		return out, true, err
	}
	var call map[string]json.RawMessage
	if err := json.Unmarshal(raw, &call); err != nil || call == nil {
		return nil, false, fmt.Errorf("%w: tool call", errInvalidSanitizeInput)
	}

	priorCtx := s.ctx
	var tool string
	_ = json.Unmarshal(call["name"], &tool)
	if tool != "" {
		s.ctx = WithRuleField(priorCtx, tool, "")
	}
	defer func() { s.ctx = priorCtx }()
	changed := false
	if fn, ok := call["function"]; ok {
		updated, didChange, err := s.sanitizeToolCallsDepth(fn, depth+1)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			call["function"], changed = updated, true
		}
	}
	if args, ok := call["arguments"]; ok {
		updated, didChange, err := s.sanitizeToolValueWithLabel(args, 0, "arguments")
		if err != nil {
			return nil, false, err
		}
		if didChange {
			call["arguments"], changed = updated, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(call)
	return out, true, err
}

func (s *requestInputSanitizer) sanitizeToolBlock(raw json.RawMessage, block map[string]json.RawMessage, field string, value json.RawMessage) (json.RawMessage, bool, error) {
	priorCtx := s.ctx
	var tool string
	_ = json.Unmarshal(block["name"], &tool)
	s.ctx = WithRuleField(priorCtx, tool, "")
	defer func() { s.ctx = priorCtx }()
	updated, changed, err := s.sanitizeToolValueWithLabel(value, 0, field)
	if err != nil || !changed {
		return raw, false, err
	}
	block[field] = updated
	out, err := json.Marshal(block)
	return out, true, err
}

// Tool arguments can be structured JSON or a JSON document embedded in a string.
// Decoding and re-encoding string leaves preserves quotes, escapes and numbers.
func (s *requestInputSanitizer) sanitizeToolValue(raw json.RawMessage, depth int) (json.RawMessage, bool, error) {
	return s.sanitizeToolValueWithLabel(raw, depth, "")
}

func (s *requestInputSanitizer) sanitizeToolValueWithLabel(raw json.RawMessage, depth int, label string) (json.RawMessage, bool, error) {
	if depth > maxNativeToolInputDepth {
		return nil, false, fmt.Errorf("%w: tool input depth", errInvalidSanitizeInput)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, false, nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, false, err
		}
		_, _, marker := parseClosedLoosePlaceholder(text)
		if marker {
			// Marker text is already a protected value. Do not feed its index or
			// type through the detector again (a short numeric suffix can itself
			// match a sensitive-data rule and mint a second placeholder).
			return raw, false, nil
		}
		managed := false
		if d, ok := s.sanitizer.detector.(*PatternDetector); ok {
			managed = d.RuleManager() != nil
		}
		if !managed && credentialField(label) && text != "" {
			typ := TypeSecret
			switch strings.ToLower(label) {
			case "account", "username", "user_name", "login", "帐号", "账号", "账户", "用户名":
				typ = TypeAccount
			}
			token, err := allocateSensitivePlaceholder(typ, text, s.offset, s.existing)
			if err != nil {
				return nil, false, err
			}
			s.mapping[token] = text
			s.replacements++
			p, _ := ParsePlaceholder(token)
			if p.Index > s.usedCount[string(typ)] {
				s.usedCount[string(typ)] = p.Index
			}
			out, err := json.Marshal(token)
			return out, true, err
		}
		doc := bytes.TrimSpace([]byte(text))
		// Managed nested strings keep their actual field and full restoration
		// boundary. Only protocol arguments/input roots carry embedded JSON
		// containers; legacy unversioned parsing retains its recursive behavior.
		structuredRoot := !managed || depth == 0 && (label == "arguments" || label == "input")
		if structuredRoot && len(doc) > 0 && (doc[0] == '{' || doc[0] == '[') && json.Valid(doc) {
			// Decoded arrays retain the containing field for their string
			// elements; object members acquire their own actual JSON keys.
			if doc[0] == '{' {
				label = ""
			}
			updated, changed, err := s.sanitizeToolValueWithLabel(doc, depth+1, label)
			if err != nil || !changed {
				return raw, false, err
			}
			out, err := json.Marshal(string(updated))
			return out, true, err
		}
		prior := s.ctx
		meta, _ := prior.Value(ruleFieldKey{}).(ruleField)
		s.ctx = WithRuleField(prior, meta.tool, label)
		defer func() { s.ctx = prior }()
		return s.sanitizeText(raw)
	case '{':
		if credentialField(label) {
			return nil, false, fmt.Errorf("%w: credential object requires a string contract", errInvalidSanitizeInput)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, false, fmt.Errorf("%w: tool input JSON", errInvalidSanitizeInput)
		}
		changed := false
		// Map order must not affect placeholder allocation or cache fingerprints.
		for _, key := range sortedRawKeys(object) {
			value := object[key]
			updated, didChange, err := s.sanitizeToolValueWithLabel(value, depth+1, key)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				object[key], changed = updated, true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(object)
		return out, true, err
	case '[':
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, false, fmt.Errorf("%w: tool input array", errInvalidSanitizeInput)
		}
		changed := false
		for i, value := range values {
			updated, didChange, err := s.sanitizeToolValueWithLabel(value, depth+1, label)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				values[i], changed = updated, true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(values)
		return out, true, err
	default:
		if credentialField(label) && !bytes.Equal(trimmed, []byte("null")) {
			return nil, false, fmt.Errorf("%w: credential requires string values", errInvalidSanitizeInput)
		}
		return raw, false, nil
	}
}
