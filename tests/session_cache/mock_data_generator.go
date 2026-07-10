// Package session_cache_test - mock_data_generator.go
//
// 模拟多轮会话数据生成器，用于测试三层缓存系统
package session_cache_test

import (
	"encoding/json"
	"fmt"
	"time"
)

// Message 表示一条聊天消息
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SessionTurn 表示会话的一轮交互
type SessionTurn struct {
	TurnNumber      int             `json:"turn_number"`
	Timestamp       time.Time       `json:"timestamp"`
	UserMessage     Message         `json:"user_message"`
	AssistantReply  Message         `json:"assistant_reply"`
	RequestBody     json.RawMessage `json:"request_body"`
	OutboundBody    json.RawMessage `json:"outbound_body"`
	ResponseBody    json.RawMessage `json:"response_body"`
	MessageCount    int             `json:"message_count"`
	TokenEstimate   int             `json:"token_estimate"`
	CompressedCount int             `json:"compressed_count,omitempty"`
}

// MockSession 表示一个完整的模拟会话
type MockSession struct {
	SessionID string        `json:"session_id"`
	TenantID  string        `json:"tenant_id"`
	Turns     []SessionTurn `json:"turns"`
	CreatedAt time.Time     `json:"created_at"`
}

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

	// 模拟一个完整的对话场景：用户询问如何使用API
	conversation := []struct {
		user      string
		assistant string
	}{
		{
			"你好，我想了解如何使用你们的API",
			"您好！我很乐意帮助您了解我们的API。我们提供RESTful API，支持多种编程语言。您想从哪方面开始了解？",
		},
		{
			"我想知道如何进行身份认证",
			"我们的API使用API Key进行身份认证。您需要在请求头中添加 Authorization: Bearer YOUR_API_KEY。您可以在控制台创建API Key。",
		},
		{
			"如何发送第一个请求？",
			"很简单！这是一个curl示例：\n```bash\ncurl -X POST https://api.example.com/v1/chat/completions \\\n  -H \"Authorization: Bearer YOUR_API_KEY\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"model\": \"gpt-4\", \"messages\": [{\"role\": \"user\", \"content\": \"Hello\"}]}'\n```",
		},
		{
			"支持哪些模型？",
			"我们支持多种模型：GPT-4、GPT-3.5-turbo、Claude-3-opus、Claude-3-sonnet等。不同模型有不同的定价和性能特点。",
		},
		{
			"如何处理错误？",
			"API会返回标准HTTP状态码。常见错误：400(参数错误)、401(认证失败)、429(超速率限制)、500(服务器错误)。响应体包含详细错误信息。",
		},
		{
			"有请求速率限制吗？",
			"是的，免费用户每分钟20次请求，付费用户根据套餐不同有不同限制。响应头会包含剩余配额信息。",
		},
		{
			"如何处理流式响应？",
			"添加 \"stream\": true 参数即可启用流式响应。您会收到Server-Sent Events格式的数据流，每个chunk包含部分响应。",
		},
		{
			"能否保存会话上下文？",
			"当然！在messages数组中包含历史消息即可。系统会自动管理上下文，并在需要时进行压缩以节省token。",
		},
		{
			"如何监控API使用情况？",
			"控制台提供详细的使用统计：请求量、token消耗、成本分析、错误率等。您还可以通过API获取统计数据。",
		},
		{
			"谢谢你的帮助！",
			"不客气！如果还有其他问题，随时联系我们的技术支持。祝您使用愉快！",
		},
		{
			"最后一个问题，支持批量请求吗？",
			"支持！您可以使用我们的batch API一次提交多个请求。批量请求会异步处理，完成后通过webhook通知您。",
		},
		{
			"明白了，再次感谢！",
			"很高兴能帮到您！如果需要更详细的文档，请访问 https://docs.example.com。祝您开发顺利！",
		},
	}

	// 构建累积的messages数组
	var allMessages []Message

	for i := 0; i < turnCount; i++ {
		turnTime := startTime.Add(time.Duration(i) * 2 * time.Minute)
		convIdx := i % len(conversation)

		// 用户消息
		userMsg := Message{
			Role:    "user",
			Content: conversation[convIdx].user,
		}
		allMessages = append(allMessages, userMsg)

		// 构建请求体（包含完整历史）
		requestBody := map[string]interface{}{
			"model":    "gpt-4",
			"messages": allMessages,
		}
		requestJSON, _ := json.Marshal(requestBody)

		// 构建outbound body（可能经过压缩）
		outboundMessages := allMessages
		if len(allMessages) > 10 {
			// 模拟压缩：保留最近10条消息
			outboundMessages = allMessages[len(allMessages)-10:]
		}
		outboundBody := map[string]interface{}{
			"model":    "gpt-4",
			"messages": outboundMessages,
		}
		outboundJSON, _ := json.Marshal(outboundBody)

		// 助手回复
		assistantMsg := Message{
			Role:    "assistant",
			Content: conversation[convIdx].assistant,
		}
		allMessages = append(allMessages, assistantMsg)

		// 构建响应体
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

		// 记录这一轮
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
