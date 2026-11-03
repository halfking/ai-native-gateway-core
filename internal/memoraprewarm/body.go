// body.go — W3：出站请求体（JSON messages）上的提取与注入（P1-2③，ADR §1）。
//
// 生产挂载点的 body 是 OpenAI chat completions / Anthropic messages 共有的
// {"messages":[{role,content}]} 形态（Anthropic 的 system 是顶层字段，本文件
// 不触碰）。所有函数 fail-open：解析失败/找不到 user 消息 ⇒ 原样返回。
package memoraprewarm

import (
	"encoding/json"
	"strings"
)

// chatMessage 是两协议 messages 数组元素的公共子形。
type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// lastUserMessage 返回 messages 数组中最后一条 user 消息（nil = 无）。
func lastUserMessage(body []byte) *chatMessage {
	var obj struct {
		Messages []chatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil
	}
	for i := len(obj.Messages) - 1; i >= 0; i-- {
		if obj.Messages[i].Role == "user" {
			m := obj.Messages[i]
			return &m
		}
	}
	return nil
}

// messageText 把 content（string 或 [{type:text,text}] 数组）拍平为纯文本。
// 空消息 / 纯非文本块 ⇒ ""。
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// ExtractQueryFromBody 取预热召回 query：最后一条 user 消息文本截前
// DefaultQueryRunes。无 user/解析失败 ⇒ ""（Trigger 侧空 query no-op）。
func ExtractQueryFromBody(body []byte) string {
	m := lastUserMessage(body)
	if m == nil {
		return ""
	}
	return truncateRunes(strings.TrimSpace(messageText(m.Content)), DefaultQueryRunes)
}

// InjectIntoMessages 把注入块追加到 messages 中最后一条 user 消息的尾部。
// string content 直接尾拼；[{type:text}] 数组形态追加一个 text block。
// injection 为空 / 无 user 消息 / 解析失败 ⇒ 原样返回 (body, false)。
//
// 追加而非替换 + 只动最后一条 user：稳定前缀（system→tools→history）字节
// 不变，prompt-cache 命中不受影响；注入内容经 RenderInjection 有界 2KB。
func InjectIntoMessages(body []byte, injection string) ([]byte, bool) {
	if injection == "" {
		return body, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, false
	}
	rawMsgs, ok := obj["messages"]
	if !ok {
		return body, false
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(rawMsgs, &msgs); err != nil || len(msgs) == 0 {
		return body, false
	}
	idx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		var m chatMessage
		if err := json.Unmarshal(msgs[i], &m); err != nil {
			continue
		}
		if m.Role == "user" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return body, false
	}
	var m chatMessage
	if err := json.Unmarshal(msgs[idx], &m); err != nil {
		return body, false
	}
	newContent, ok := appendToContent(m.Content, injection)
	if !ok {
		return body, false
	}
	newMsg, err := json.Marshal(map[string]json.RawMessage{
		"role":    json.RawMessage(`"user"`),
		"content": newContent,
	})
	if err != nil {
		return body, false
	}
	msgs[idx] = newMsg
	rawMsgs, err = json.Marshal(msgs)
	if err != nil {
		return body, false
	}
	obj["messages"] = rawMsgs
	out, err := json.Marshal(obj)
	if err != nil {
		return body, false
	}
	return out, true
}

// appendToContent 把注入块拼到 content 尾部（string / 文本块数组两形态）。
// 其他形态（nil、空数组）⇒ 不动。
func appendToContent(raw json.RawMessage, injection string) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		merged, err := json.Marshal(s + "\n\n" + injection)
		if err != nil {
			return nil, false
		}
		return merged, true
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) == 0 {
		return nil, false
	}
	blk, err := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: injection})
	if err != nil {
		return nil, false
	}
	blocks = append(blocks, blk)
	merged, err := json.Marshal(blocks)
	if err != nil {
		return nil, false
	}
	return merged, true
}
