package sessionforensics

import (
	"encoding/json"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// MutationKind 标识不同的 mutation 类型。
// ─────────────────────────────────────────────────────────────────────────────

type MutationKind string

const (
	MKindModelSwap        MutationKind = "M1_model_swap"
	MKindToolTruncated    MutationKind = "M2_tool_truncated"
	MKindCutAndAppend     MutationKind = "M3_cut_and_append"
	MKindThinkingInject   MutationKind = "M4_thinking_inject"
	MKindVisionContent    MutationKind = "M5_vision_content"
	MKindLongSystemPrompt MutationKind = "M6_long_system_prompt"
	MKindEmptyMessagesAt  MutationKind = "M7_empty_messages_at"
)

// Mutation 是一次变换：Mutate 会改写 SessionPack.Messages。
type Mutation struct {
	Kind   MutationKind
	AtTurn int    // 起始 turn（1-based）；0 表示全局
	Extra  string // 用于额外参数（new model、append prompt 等）
}

// MutationReport 单个 mutation 的输出（与 baseline 对比）。
type MutationReport struct {
	Kind        MutationKind    `json:"kind"`
	Extra       string          `json:"extra,omitempty"`
	BaselineAgg ReplayAggregate `json:"baseline"`
	MutatedAgg  ReplayAggregate `json:"mutated"`
	StepsDelta  map[string]int  `json:"steps_delta"`
	BytesDelta  int             `json:"bytes_delta"`
	Notes       []string        `json:"notes"`
}

// ErrInvalidTurn is returned by Mutate when AtTurn is out of range.
var ErrInvalidTurn = fmt.Errorf("sessionforensics: invalid turn index")

// Mutate 应用一次 transformation 到 *SessionPack。
func Mutate(pack *SessionPack, m Mutation) error {
	if pack == nil {
		return fmt.Errorf("sessionforensics: nil pack")
	}
	if m.AtTurn > len(pack.Messages) {
		return fmt.Errorf("sessionforensics: turn %d out of range (max %d)",
			m.AtTurn, len(pack.Messages))
	}
	switch m.Kind {
	case MKindModelSwap:
		return mutateModelSwap(pack, m)
	case MKindToolTruncated:
		return mutateToolTruncated(pack, m)
	case MKindCutAndAppend:
		return mutateCutAndAppend(pack, m)
	case MKindThinkingInject:
		return mutateThinkingInject(pack, m)
	case MKindVisionContent:
		return mutateVisionContent(pack, m)
	case MKindLongSystemPrompt:
		return mutateLongSystemPrompt(pack, m)
	case MKindEmptyMessagesAt:
		return mutateEmptyMessagesAt(pack, m)
	default:
		return fmt.Errorf("sessionforensics: unknown mutation kind %q", m.Kind)
	}
}

// ── M1 ────────────────────────────────────────────────────────────────────

// mutateModelSwap — 在 AtTurn 之后把所有 turn 的 request_body model 字段
// 改成 Extra（默认 "claude-sonnet-5"）。
func mutateModelSwap(pack *SessionPack, m Mutation) error {
	newModel := m.Extra
	if newModel == "" {
		newModel = "claude-sonnet-5"
	}
	for i := m.AtTurn - 1; i < len(pack.Messages); i++ {
		pack.Messages[i] = rewriteModelField(pack.Messages[i], newModel)
	}
	return nil
}

// ── M2 ────────────────────────────────────────────────────────────────────

// mutateToolTruncated — 在 AtTurn 把后续 turn 的 tools 数组删除。
func mutateToolTruncated(pack *SessionPack, m Mutation) error {
	for i := m.AtTurn - 1; i < len(pack.Messages); i++ {
		body, err := decodeBody(pack.Messages[i])
		if err != nil {
			continue
		}
		delete(body, "tools")
		pack.Messages[i] = encodeBody(pack.Messages[i], body)
	}
	return nil
}

// ── M3 ────────────────────────────────────────────────────────────────────

// mutateCutAndAppend — 删除 AtTurn..AtTurn+N-1 的 turns，在末尾追加一个
// user/assistant 对（Extra 是 user msg content）。
func mutateCutAndAppend(pack *SessionPack, m Mutation) error {
	if m.AtTurn < 1 || m.AtTurn > len(pack.Messages)+1 {
		return ErrInvalidTurn
	}
	const cutN = 2
	if m.Extra == "" {
		m.Extra = "请总结前面对话并给我下一步建议"
	}
	head := pack.Messages[:m.AtTurn-1]
	tail := pack.Messages[m.AtTurn-1+cutN:]

	if len(head) == 0 {
		pack.Messages = append(head, tail...)
		return nil
	}
	tmplTurn := head[len(head)-1]
	userBody := cloneBody(tmplTurn)
	setMessagesFromString(userBody, []messageSkeleton{{Role: "user", Content: m.Extra}})
	userTurn := encodeBody(tmplTurn, userBody)
	userTurn.Turn = len(head) + 1

	asstBody := cloneBody(tmplTurn)
	setMessagesFromString(asstBody, []messageSkeleton{{Role: "assistant", Content: "好的，根据前面的对话我建议……"}})
	asstTurn := encodeBody(tmplTurn, asstBody)
	asstTurn.Turn = userTurn.Turn + 1

	rebuilt := make([]ExportMessage, 0, len(head)+2+len(tail))
	rebuilt = append(rebuilt, head...)
	rebuilt = append(rebuilt, userTurn, asstTurn)
	for j := range tail {
		tail[j] = reNumberTurn(tail[j], userTurn.Turn+1+j+1)
		rebuilt = append(rebuilt, tail[j])
	}
	pack.Messages = rebuilt
	return nil
}

// ── M4 ────────────────────────────────────────────────────────────────────

// mutateThinkingInject — 在 AtTurn 的 assistant 消息最前面塞入巨大 thinking
// block（v4 strip 应该能把这些清掉）。
func mutateThinkingInject(pack *SessionPack, m Mutation) error {
	if m.AtTurn < 1 || m.AtTurn > len(pack.Messages) {
		return ErrInvalidTurn
	}
	idx := m.AtTurn - 1
	body, err := decodeBody(pack.Messages[idx])
	if err != nil {
		return err
	}
	msgs := getMessages(body)
	if len(msgs) == 0 {
		return nil
	}
	thinkingText := "<thinking>" + repeatString(
		"用户可能想让我反思一下。从历史对话看，最近的请求是关于 ", 200) + "</thinking>"
	thinkingJSON, _ := json.Marshal(thinkingText)
	for j := range msgs {
		if getString(msgs[j], "role") == "assistant" {
			oldContent := msgs[j]["content"]
			// 用 JSON 数组包裹，这样 v4 strip 能识别
			msgs[j]["content"] = json.RawMessage(
				`[{"type":"thinking","text":` + string(thinkingJSON) + `},` +
					trimContentToJSON(oldContent) + `]`)
		}
	}
	setMessages(body, msgs)
	pack.Messages[idx] = encodeBody(pack.Messages[idx], body)
	return nil
}

// ── M5 ────────────────────────────────────────────────────────────────────

// mutateVisionContent — 把 AtTurn 后所有 user 消息的 content 改成 vision
// 风格（数组 + image_url 占位）。
func mutateVisionContent(pack *SessionPack, m Mutation) error {
	for i := m.AtTurn - 1; i < len(pack.Messages); i++ {
		body, err := decodeBody(pack.Messages[i])
		if err != nil {
			continue
		}
		msgs := getMessages(body)
		for j := range msgs {
			if getString(msgs[j], "role") == "user" {
				old := msgs[j]["content"]
				oldJSON, _ := json.Marshal(old)
				visionContent := json.RawMessage(fmt.Sprintf(
					`[{"type":"text","text":%s},{"type":"image_url","image_url":{"url":"https://example.com/img-%d-%d.png"}}]`,
					string(oldJSON), i, j))
				msgs[j]["content"] = visionContent
			}
		}
		setMessages(body, msgs)
		pack.Messages[i] = encodeBody(pack.Messages[i], body)
	}
	return nil
}

// ── M6 ────────────────────────────────────────────────────────────────────

// mutateLongSystemPrompt — 把所有 turn 的 system prompt 扩到 Extra 长
// （默认 50KB），模拟 context length 接近上限场景。
func mutateLongSystemPrompt(pack *SessionPack, m Mutation) error {
	targetLen := 50_000
	if m.Extra != "" {
		var n int
		_, _ = fmt.Sscanf(m.Extra, "%d", &n)
		if n > 0 {
			targetLen = n
		}
	}
	for i := range pack.Messages {
		body, err := decodeBody(pack.Messages[i])
		if err != nil {
			continue
		}
		msgs := getMessages(body)
		for j := range msgs {
			if getString(msgs[j], "role") == "system" {
				msgs[j]["content"] = json.RawMessage(fmt.Sprintf(
					`"%s ..."`, repeatString("you are helpful, ", targetLen/22)))
			}
		}
		setMessages(body, msgs)
		pack.Messages[i] = encodeBody(pack.Messages[i], body)
	}
	return nil
}

// ── M7 ────────────────────────────────────────────────────────────────────

// mutateEmptyMessagesAt — 把 AtTurn 的 messages 数组清空（异常情况）。
func mutateEmptyMessagesAt(pack *SessionPack, m Mutation) error {
	if m.AtTurn < 1 || m.AtTurn > len(pack.Messages) {
		return ErrInvalidTurn
	}
	idx := m.AtTurn - 1
	body, err := decodeBody(pack.Messages[idx])
	if err != nil {
		return err
	}
	setMessages(body, nil)
	pack.Messages[idx] = encodeBody(pack.Messages[idx], body)
	return nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func rewriteModelField(t ExportMessage, newModel string) ExportMessage {
	body, err := decodeBody(t)
	if err != nil {
		return t
	}
	b, _ := json.Marshal(newModel)
	body["model"] = b
	return encodeBody(t, body)
}

func decodeBody(t ExportMessage) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if len(t.Content) == 0 {
		return nil, fmt.Errorf("empty content")
	}
	if err := json.Unmarshal([]byte(t.Content), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func encodeBody(t ExportMessage, body map[string]json.RawMessage) ExportMessage {
	out, _ := json.Marshal(body)
	t.Content = string(out)
	return t
}

func cloneBody(t ExportMessage) map[string]json.RawMessage {
	body, err := decodeBody(t)
	if err != nil {
		return map[string]json.RawMessage{}
	}
	out := make(map[string]json.RawMessage, len(body))
	for k, v := range body {
		out[k] = v
	}
	return out
}

func getMessages(body map[string]json.RawMessage) []map[string]json.RawMessage {
	if raw, ok := body["messages"]; ok {
		var out []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &out); err == nil {
			return out
		}
	}
	return nil
}

func setMessages(body map[string]json.RawMessage, msgs []map[string]json.RawMessage) {
	if msgs == nil {
		body["messages"] = json.RawMessage(`[]`)
		return
	}
	b, _ := json.Marshal(msgs)
	body["messages"] = b
}

func setMessagesFromString(body map[string]json.RawMessage, skel []messageSkeleton) {
	msgs := make([]map[string]json.RawMessage, 0, len(skel))
	for _, s := range skel {
		c, _ := json.Marshal(s.Content)
		msgs = append(msgs, map[string]json.RawMessage{
			"role":    json.RawMessage(fmt.Sprintf(`"%s"`, s.Role)),
			"content": c,
		})
	}
	setMessages(body, msgs)
}

type messageSkeleton struct {
	Role, Content string
}

func getString(m map[string]json.RawMessage, key string) string {
	if v, ok := m[key]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s
		}
	}
	return ""
}

// trimContentToJSON 把 json.RawMessage 包成 JSON 数组元素（"或{...}"）。
func trimContentToJSON(rm json.RawMessage) string {
	s := string(rm)
	if s == "" {
		s = `""`
	}
	return s
}

func repeatString(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func reNumberTurn(t ExportMessage, newTurn int) ExportMessage {
	t.Turn = newTurn
	return t
}
