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

	head, firstUser, tail := splitHeadAndTail(probe.Messages)
	if head == nil {
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
func splitHeadAndTail(msgs []json.RawMessage) (head []json.RawMessage, firstUser []json.RawMessage, tail []json.RawMessage) {
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
		// 没有 user 消息：全部当 system 头，不做装配（避免造出畸形数组）。
		return nil, nil, nil
	}
	head = append(head, msgs[:firstUserIdx]...)
	firstUser = []json.RawMessage{msgs[firstUserIdx]}
	tail = msgs[firstUserIdx+1:]
	return head, firstUser, tail
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

// isSpaceByte 判断是不是 JSON 空白。
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// indexOfKey 找到顶层对象里某个键的位置（要求键前面是 '{' 或 ','）。
func indexOfKey(body, key []byte) int {
	for i := 0; i+len(key) <= len(body); {
		if body[i] == '"' && i+len(key) < len(body) && string(body[i:i+len(key)]) == string(key) {
			if i > 0 {
				k := i - 1
				for k > 0 && isSpaceByte(body[k]) {
					k--
				}
				if body[k] != '{' && body[k] != ',' {
					i++
					continue
				}
			}
			j := i + len(key)
			for j < len(body) && isSpaceByte(body[j]) {
				j++
			}
			if j < len(body) && body[j] == ':' {
				return i
			}
		}
		i++
	}
	return -1
}
