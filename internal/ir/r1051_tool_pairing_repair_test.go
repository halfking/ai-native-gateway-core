package ir

// zz2_tool_pairing_test.go — 2026-11-13（211 错误盘点轮）：openai chat 入站
// tool 消息缺 tool_call_id 的配对修复钉测。事故形态（211 request_wal_hot
// 10:37 一簇）：ZCode 会话重放的 messages[N] tool role 缺 tool_call_id ⇒
// 同协议直传格式门 fail-closed 拒绝 ⇒ 重放同败 ⇒ 会话砖化。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/protocolformat"
)

func TestRepairChatToolCallIDs_BackfillFromAssistant(t *testing.T) {
	body := []byte(`{"model":"glm-5.3-flash","messages":[
		{"role":"user","content":"run the tool"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","content":"result A"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_b","type":"function","function":{"name":"g","arguments":"{}"}}]},
		{"role":"tool","content":"result B"}
	]}`)
	req, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var got []string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			got = append(got, m.ToolCallID)
		}
	}
	if len(got) != 2 || got[0] != "call_a" || got[1] != "call_b" {
		t.Fatalf("backfilled ids = %v, want [call_a call_b] (order-paired from nearest assistant tool_calls)", got)
	}
	// 修复后必须能过同协议格式门。
	if err := protocolValidateOpenAIForTest(req); err != nil {
		t.Fatalf("format gate after repair: %v", err)
	}
}

func TestRepairChatToolCallIDs_ExplicitIDConsumesQueue(t *testing.T) {
	// 显式 id 的 tool 消息先消费队列，缺 id 的拿到下一个。
	body := []byte(`{"model":"m","messages":[
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"a","arguments":"{}"}},
			{"id":"call_2","type":"function","function":{"name":"b","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"first"},
		{"role":"tool","content":"second"}
	]}`)
	req, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var missing []string
	for _, m := range req.Messages {
		if m.Role == "tool" && m.ToolCallID == "" {
			missing = append(missing, m.Content[0].Text)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("tool message left without id: %v", missing)
	}
	second := req.Messages[2]
	if second.ToolCallID != "call_2" {
		t.Fatalf("second tool should pair call_2 (call_1 consumed explicitly), got %q", second.ToolCallID)
	}
}

func TestRepairChatToolCallIDs_OrphanDowngradesToUserText(t *testing.T) {
	// 无前置 assistant tool_calls ⇒ 降级为 user 文本，内容保留。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"tool","content":"orphan result"},
		{"role":"user","content":"continue"}
	]}`)
	req, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var toolCount int
	var downgradedText string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			toolCount++
		}
		if m.Role == "user" && strings.Contains(messageTextContent(&m), "orphan result") {
			downgradedText = messageTextContent(&m)
		}
	}
	if toolCount != 0 {
		t.Fatalf("orphan tool message must be downgraded (role no longer tool)")
	}
	if !strings.Contains(downgradedText, "[tool result without tool_call_id]") {
		t.Fatalf("downgraded text should carry annotation, got %q", downgradedText)
	}
}

func protocolValidateOpenAIForTest(req *InternalRequest) error {
	// 重建 openai chat wire 形态后跑格式门（validate 需要 tool 角色带 id）。
	var msgs []map[string]any
	for _, m := range req.Messages {
		msg := map[string]any{"role": m.Role, "content": messageTextContent(&m)}
		if m.Role == "tool" {
			msg["tool_call_id"] = m.ToolCallID
		}
		msgs = append(msgs, msg)
	}
	body, err := json.Marshal(map[string]any{"model": req.Model, "messages": msgs})
	if err != nil {
		return err
	}
	return protocolformat.ValidateOpenAIChatRequest(body)
}
