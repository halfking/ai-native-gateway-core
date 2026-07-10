// Package session_cache_test - mock_data_generator.go
//
// 模拟多轮会话数据生成器，用于测试三层缓存系统
package session_cache_test

import (
	"encoding/json"
	"fmt"
	"time"
)

// GenerateMockSessions 生成多个模拟会话用于测试
func GenerateMockSessions(count int) []MockSession {
	sessions := make([]MockSession, count)
	baseTime := time.Now().Add(-24 * time.Hour)

	for i := 0; i < count; i++ {
		sessions[i] = GenerateSingleSession(
			fmt.Sprintf("gw_test_%d", i+1),
			"tenant_test_001",
			baseTime.Add(time.Duration(i)*time.Hour),
			5+i%10, // 5-14轮对话
		)
	}

	return sessions
}

// GenerateSingleSession 生成单个会话的多轮对话
func GenerateSingleSession(sessionID, tenantID string, startTime time.Time, turnCount int) MockSession {
	session := MockSession{
		SessionID: sessionID,
		TenantID:  tenantID,
		CreatedAt: startTime,
		Turns:     make([]SessionTurn, turnCount),
	}

	conversation := []struct {
		user      string
		assistant string
	}{
		{"你好，我想了解如何使用你们的API", "您好！我很乐意帮助您了解我们的API。"},
		{"我想知道如何进行身份认证", "我们的API使用API Key进行身份认证。"},
		{"如何发送第一个请求？", "很简单！这是一个curl示例..."},
		{"支持哪些模型？", "我们支持多种模型：GPT-4、GPT-3.5-turbo等。"},
		{"如何处理错误？", "API会返回标准HTTP状态码。"},
		{"有请求速率限制吗？", "是的，免费用户每分钟20次请求。"},
		{"如何处理流式响应？", "添加 stream: true 参数即可启用流式响应。"},
		{"能否保存会话上下文？", "当然！在messages数组中包含历史消息即可。"},
		{"如何监控API使用情况？", "控制台提供详细的使用统计。"},
		{"谢谢你的帮助！", "不客气！如果还有其他问题，随时联系。"},
		{"最后一个问题，支持批量请求吗？", "支持！您可以使用我们的batch API。"},
		{"明白了，再次感谢！", "很高兴能帮到您！"},
	}

	var allMessages []Message

	for i := 0; i < turnCount; i++ {
		turnTime := startTime.Add(time.Duration(i) * 2 * time.Minute)
		convIdx := i % len(conversation)

		userMsg := Message{Role: "user", Content: conversation[convIdx].user}
		allMessages = append(allMessages, userMsg)

		requestBody := map[string]interface{}{
			"model":    "gpt-4",
			"messages": allMessages,
		}
		requestJSON, _ := json.Marshal(requestBody)

		outboundMessages := allMessages
		if len(allMessages) > 10 {
			outboundMessages = allMessages[len(allMessages)-10:]
		}
		outboundBody := map[string]interface{}{
			"model":    "gpt-4",
			"messages": outboundMessages,
		}
		outboundJSON, _ := json.Marshal(outboundBody)

		assistantMsg := Message{Role: "assistant", Content: conversation[convIdx].assistant}
		allMessages = append(allMessages, assistantMsg)

		responseBody := map[string]interface{}{
			"id":      fmt.Sprintf("chatcmpl-%d", i+1),
			"object":  "chat.completion",
			"created": turnTime.Unix(),
			"model":   "gpt-4",
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]string{
						"role":    "assistant",
						"content": assistantMsg.Content,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     len(userMsg.Content) * 4 / 3,
				"completion_tokens": len(assistantMsg.Content) * 4 / 3,
				"total_tokens":      (len(userMsg.Content) + len(assistantMsg.Content)) * 4 / 3,
			},
		}
		responseJSON, _ := json.Marshal(responseBody)

		session.Turns[i] = SessionTurn{
			TurnNumber:      i + 1,
			Timestamp:       turnTime,
			UserMessage:     userMsg,
			AssistantReply:  assistantMsg,
			RequestBody:     requestJSON,
			OutboundBody:    outboundJSON,
			ResponseBody:    responseJSON,
			MessageCount:    len(allMessages),
			TokenEstimate:   (len(userMsg.Content) + len(assistantMsg.Content)) * 4 / 3,
			CompressedCount: len(outboundMessages),
		}
	}

	return session
}

// ExportToJSONL 将会话导出为JSONL格式
func (s *MockSession) ExportToJSONL() ([]byte, error) {
	lines := make([][]byte, len(s.Turns))
	for i, turn := range s.Turns {
		record := map[string]interface{}{
			"session_id":       s.SessionID,
			"tenant_id":        s.TenantID,
			"turn_number":      turn.TurnNumber,
			"timestamp":        turn.Timestamp,
			"request_body":     json.RawMessage(turn.RequestBody),
			"outbound_body":    json.RawMessage(turn.OutboundBody),
			"response_body":    json.RawMessage(turn.ResponseBody),
			"message_count":    turn.MessageCount,
			"token_estimate":   turn.TokenEstimate,
			"compressed_count": turn.CompressedCount,
		}
		line, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		lines[i] = line
	}

	result := []byte{}
	for i, line := range lines {
		result = append(result, line...)
		if i < len(lines)-1 {
			result = append(result, '\n')
		}
	}

	return result, nil
}
