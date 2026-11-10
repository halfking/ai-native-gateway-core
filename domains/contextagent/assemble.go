package contextagent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// IndexHeader 是 L0 索引层的抬头。
//
// ★ 抬头与条目用**固定前缀**标记，让下游（模型、运维、审计）能稳定判别
//
//	「这段是网关装配的上下文」，与生产既有的
//	domains/hooks/compression 的 CompressionSummaryPrefix 同一思路。
const IndexHeader = "【执行摘要索引（每一步的目标与结果）】"

// DetailHeader 是按需加载的步骤明细块的抬头。
const DetailHeader = "【按需加载的步骤明细（来自网关会话存储）】"

// LongTermHeader 是 L2 长期记忆块的抬头。
const LongTermHeader = "【长期记忆参考（语义召回；可能与当前任务无关，仅供上下文）】"

// KBHeader 是知识库块的抬头。
const KBHeader = "【知识库参考】"

// RenderIndex 把索引条目渲染成 L0 索引文本。
//
// ★ 一行一步，保留「目标 → 结果」；条目里的标识符（路径 / 错误码）由调用方
//
//	决定是否清洗——本函数**只做长度保护**，不做语义改写。
func RenderIndex(entries []IndexEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(IndexHeader)
	b.WriteString("\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- 步%d | 目标: %s | 结果: %s\n", e.StepIndex, clip(e.Goal, 200), clip(e.Result, 300))
	}
	return b.String()
}

// RenderStepDetails 把若干步的明细渲染成一个注入块。
func RenderStepDetails(details []StepDetail) string {
	if len(details) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(DetailHeader)
	b.WriteString("\n")
	for _, d := range details {
		fmt.Fprintf(&b, "【步%d】\n%s\n", d.StepIndex, messagesText(d.Messages))
	}
	return b.String()
}

// RenderLongTerm 把 L2 召回结果渲染成一个注入块。
func RenderLongTerm(items []string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(LongTermHeader)
	b.WriteString("\n")
	for i, it := range items {
		fmt.Fprintf(&b, "- 记忆%d: %s\n", i+1, clip(it, 480))
	}
	return b.String()
}

// clip 按字符数裁剪（不切断语义；不补省略号，避免污染标识符）。
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max])
}

// messagesText 把消息数组渲染成可读文本。
func messagesText(msgs []map[string]any) string {
	var b strings.Builder
	for _, m := range msgs {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		if content == "" {
			if calls, ok := m["tool_calls"].([]any); ok && len(calls) > 0 {
				content = fmt.Sprintf("[tool_calls x%d]", len(calls))
			}
		}
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", role, content)
	}
	return b.String()
}

// Assemble 把各层装配成最终的出站请求体。
//
// ★ 装配形状（与生产既有能力同形，见 domains/hooks/compression/rebuilder_openai.go
//
//	  的「A-track system + C-track 摘要 + B-track 首条 user + 尾部」）：
//
//		[system 消息们]
//		[L0 索引]                ← 最上层（用户要的位置）
//		[L2 长期记忆]（可选）
//		[L1 步骤明细]（可选，按需加载）
//		[首条 user（B-track 钉住）]
//		[最近 N 条原始消息]
//
// ★ 首条 user 消息被**钉住**（B-track）：它是用户的原始意图，
//
//	压缩不能把它丢掉——这是生产既有语义，本包沿用。
//
// ★ 原始 system 消息**原样保留**，不做任何改写。
//
// 返回 false 表示「装配没有产生任何变化」⇒ 调用方应继续用原体
// （避免无谓的重序列化，破坏字节级保真）。
func Assemble(original []byte, layers []LayerContent) ([]byte, bool) {
	if len(layers) == 0 {
		return original, false
	}
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(original, &probe); err != nil || len(probe.Messages) == 0 {
		return original, false
	}

	head, firstUser, tail, ok := splitHeadAndTail(probe.Messages)
	if !ok {
		// ★ 没有 user 消息 ⇒ 锚不住 B-track ⇒ 不做装配。
		//	（判据是 ok 标志，**不是** head == nil —— 后者会把
		//	「首条就是 user、没有 system 头」这种最常见的形状误伤成
		//	「装配失效」；审计轮实测那种形状下装配恒为 no-op。）
		return original, false
	}

	merged := make([]json.RawMessage, 0, len(head)+len(layers)+len(tail)+2)
	merged = append(merged, head...)

	for _, lc := range layers {
		if strings.TrimSpace(lc.Text) == "" {
			continue
		}
		raw, err := json.Marshal(map[string]any{
			"role":    "user",
			"content": lc.Text,
		})
		if err != nil {
			continue
		}
		merged = append(merged, raw)
	}

	// B-track：首条 user 消息钉在索引之后（与生产 rebuilder 同序）。
	merged = append(merged, firstUser...)
	merged = append(merged, tail...)

	newMsgs, err := json.Marshal(merged)
	if err != nil {
		return original, false
	}
	spliced, ok := spliceMessages(original, newMsgs)
	if !ok {
		return original, false
	}
	return spliced, true
}

// splitHeadAndTail 把消息数组切成三段：system 头 + 首条 user（B-track）+ 尾部。
//
// ★ 与生产既有 rebuilder 的 splitSystemAndTail 同思路，但**多切一刀**：
//
//	head      = 开头的 system 消息们
//	firstUser = 第一条 user 消息（B-track 钉住）
//	tail      = 其余消息
//
//	生产把「system + 首条 user」一起当 head；本包要保证索引落在
//	**system 之后、首条 user 之前**（用户要的「最上层」），所以首条 user
//	被单独摘出来。
//
// ★ 返回的 ok 表示「锚到了首条 user」；**不要**用 head == nil 当判据：
//
//	首条即 user（没有 system 头）时 head 合法地是空的，
//	旧实现据此判定「装配失效」⇒ 最常见的请求形状永远拿不到任何一层
//	（审计轮实测：ok=false、Assembled 恒 false、而 PG/Memora 的查询照跑）。
//
//	head 允许为空（没有 system 消息），firstUser 一定非空。
func splitHeadAndTail(msgs []json.RawMessage) (head []json.RawMessage, firstUser []json.RawMessage, tail []json.RawMessage, ok bool) {
	firstUserIdx := -1
	i := 0
	for ; i < len(msgs); i++ {
		role := rawRole(msgs[i])
		if role == "system" {
			continue
		}
		if role == "user" {
			firstUserIdx = i
		}
		break
	}
	if firstUserIdx < 0 {
		// 没有 user 消息：锚不住 B-track，不做装配（避免造出畸形数组）。
		return nil, nil, nil, false
	}
	head = append(head, msgs[:firstUserIdx]...)
	firstUser = []json.RawMessage{msgs[firstUserIdx]}
	tail = msgs[firstUserIdx+1:]
	return head, firstUser, tail, true
}

// rawRole 取一条消息的角色。
func rawRole(raw json.RawMessage) string {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.Role
}

// spliceMessages 把原请求体里的 messages 数组整段替换掉，
// 其余顶层键（model / stream / tools / temperature …）原样保留。
//
// ★ 用「字节级切片替换」而不是反序列化再序列化
//
//	（与 domains/hooks/compression/rebuilder_openai.go 的 spliceMessagesRaw 同思路）：
//	这样 model/stream 等键的**原始字节**不变，减少对传输契约的扰动。
func spliceMessages(original, newMsgs []byte) ([]byte, bool) {
	key := []byte(`"messages"`)
	idx := indexOfKey(original, key)
	if idx < 0 {
		return nil, false
	}
	i := idx + len(key)
	for i < len(original) && isSpaceByte(original[i]) {
		i++
	}
	if i >= len(original) || original[i] != ':' {
		return nil, false
	}
	i++
	for i < len(original) && isSpaceByte(original[i]) {
		i++
	}
	if i >= len(original) || original[i] != '[' {
		return nil, false
	}
	end := i + 1
	depth := 1
	inStr := false
	escape := false
	for end < len(original) {
		c := original[end]
		if inStr {
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inStr = false
			}
		} else {
			switch c {
			case '"':
				inStr = true
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					goto found
				}
			}
		}
		end++
	}
	return nil, false
found:
	out := make([]byte, 0, len(original)-end+len(newMsgs))
	out = append(out, original[:i]...)
	out = append(out, newMsgs...)
	out = append(out, original[end+1:]...)
	return out, true
}

// forceNonStream 把顶层 "stream" 键改写为 false。
//
// ★ 为什么需要：记忆工具回环要从上游响应里**识别工具调用**，
//
//	而 SSE（stream:true）响应不是单个 JSON ⇒ 识别恒失败
//	⇒ 回环静默失效（审计轮实测：SSE 形态识别数恒为 0）。
//	⇒ 「网关驱动形态」（服务端跑完整循环）必须出站非流式。
//
// ★ 字节级改写，不重序列化整个体（与本包其它拼接同一口径）。
//
// ★ 顶层键定位用深度感知的 indexOfKey ⇒ 嵌套的 "stream" 不会被改。
func forceNonStream(body []byte) ([]byte, bool) {
	key := []byte(`"stream"`)
	idx := indexOfKey(body, key)
	if idx < 0 {
		return body, false
	}
	i := idx + len(key)
	for i < len(body) && isSpaceByte(body[i]) {
		i++
	}
	if i >= len(body) || body[i] != ':' {
		return body, false
	}
	i++
	for i < len(body) && isSpaceByte(body[i]) {
		i++
	}
	j := i
	for j < len(body) && !isSpaceByte(body[j]) && body[j] != ',' && body[j] != '}' {
		j++
	}
	if j == i {
		return body, false
	}
	if strings.TrimSpace(string(body[i:j])) == "false" {
		return body, false // 已经是 false
	}
	out := make([]byte, 0, len(body)+1)
	out = append(out, body[:i]...)
	out = append(out, "false"...)
	out = append(out, body[j:]...)
	return out, true
}

// isSpaceByte 判断是不是 JSON 空白。
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// indexOfKey 找到**顶层**对象里某个键的位置。
//
// ★ 深度感知（2026-11-11 审计轮）：只接受**深度 1**（顶层对象内）的键。
//
//	原实现只看「键前面一个字节是 { 或 ,」⇒ **嵌套**对象里同名的键也会
//	被优先命中。实测：
//
//	输入 {"metadata":{"messages":["decoy"]},"model":"gpt-4","messages":[...]}
//
//	⇒ 命中的**是** **metadata.messages** ⇒ 顶层 messages 原封未动
//	   （记忆层被静默丢弃），而嵌套数组被替换成整份新消息数组
//	   （出站体被改坏）。
//	同一函数还被 spliceOrInsertTopLevelArray 复用 ⇒ "tools" 注入同源缺陷。
//
// ★ 同时跟踪**字符串态**（含反斜杠转义）：键名可能出现在字符串值里，
//
//	例如 {"note":"\"messages\": 1","messages":[...]} ⇒ 不能把值里的
//	那串当成键。
//
// ★ 深度计数口径：顶层对象的 '{' 把深度推进到 1 ⇒ 顶层对象的键在深度 1；
//
//	嵌套对象/数组把深度推得更深 ⇒ 其键被排除。
func indexOfKey(body, key []byte) int {
	depth := 0
	inStr := false
	escape := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inStr {
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			if depth == 1 && i+len(key) <= len(body) && string(body[i:i+len(key)]) == string(key) {
				j := i + len(key)
				for j < len(body) && isSpaceByte(body[j]) {
					j++
				}
				if j < len(body) && body[j] == ':' {
					return i
				}
			}
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return -1
}
