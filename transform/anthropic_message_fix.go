package transform

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// FixAnthropicMessages 修复 Anthropic Messages API 的常见问题:
// 1. 将 "tool" 角色转换为 "user" + tool_result block
// 2. 合并连续的同角色消息
// 3. 确保最后一条消息是 user
func FixAnthropicMessages(body []byte) ([]byte, error) {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	messagesRaw, ok := req["messages"]
	if !ok {
		return body, nil
	}

	messages, ok := messagesRaw.([]interface{})
	if !ok {
		return body, nil
	}

	// Step 1: 转换 tool 角色
	messages, toolFixed := convertToolRolesToUser(messages)
	if toolFixed > 0 {
		slog.Warn("anthropic_message_fix: converted tool roles to user",
			"count", toolFixed,
		)
	}

	// Step 2: 合并连续同角色消息
	messages, merged := mergeConsecutiveSameRole(messages)
	if merged > 0 {
		slog.Warn("anthropic_message_fix: merged consecutive same-role messages",
			"count", merged,
		)
	}

	req["messages"] = messages
	return json.Marshal(req)
}

// convertToolRolesToUser 将 "tool" 角色转换为 "user" 角色 + tool_result content block
func convertToolRolesToUser(messages []interface{}) ([]interface{}, int) {
	fixed := 0
	result := make([]interface{}, 0, len(messages))

	for _, msgRaw := range messages {
		msg, ok := msgRaw.(map[string]interface{})
		if !ok {
			result = append(result, msgRaw)
			continue
		}

		role, _ := msg["role"].(string)
		if role == "tool" {
			// 转换为 user 消息
			content := msg["content"]
			var contentStr string
			switch v := content.(type) {
			case string:
				contentStr = v
			default:
				contentStr = fmt.Sprintf("%v", v)
			}

			fixedMsg := map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "unknown_tool",
						"content":     contentStr,
					},
				},
			}
			result = append(result, fixedMsg)
			fixed++
		} else {
			result = append(result, msg)
		}
	}

	return result, fixed
}

// mergeConsecutiveSameRole 合并连续的同角色消息
func mergeConsecutiveSameRole(messages []interface{}) ([]interface{}, int) {
	if len(messages) == 0 {
		return messages, 0
	}

	merged := 0
	result := []interface{}{messages[0]}

	for i := 1; i < len(messages); i++ {
		lastMsg, _ := result[len(result)-1].(map[string]interface{})
		currMsg, ok := messages[i].(map[string]interface{})
		if !ok {
			result = append(result, messages[i])
			continue
		}

		lastRole, _ := lastMsg["role"].(string)
		currRole, _ := currMsg["role"].(string)

		// system 消息不合并
		if lastRole == currRole && lastRole != "system" {
			// 合并到上一条消息
			lastContent := ensureContentArray(lastMsg["content"])
			currContent := ensureContentArray(currMsg["content"])
			lastMsg["content"] = append(lastContent, currContent...)
			merged++
		} else {
			result = append(result, currMsg)
		}
	}

	return result, merged
}

// ensureContentArray 确保 content 是数组格式
func ensureContentArray(content interface{}) []interface{} {
	switch v := content.(type) {
	case []interface{}:
		return v
	case string:
		return []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": v,
			},
		}
	default:
		return []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": fmt.Sprintf("%v", v),
			},
		}
	}
}

// ValidateAnthropicMessages 验证消息序列是否符合 Anthropic API 规范
func ValidateAnthropicMessages(body []byte) error {
	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return err
	}

	// 检查1: 不支持的角色
	for i, msg := range req.Messages {
		if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "system" {
			return fmt.Errorf("unsupported role at message %d: %s (Claude only supports user/assistant/system)", i+1, msg.Role)
		}
	}

	// 检查2: 连续同角色（system 除外）
	for i := 1; i < len(req.Messages); i++ {
		prev := req.Messages[i-1]
		curr := req.Messages[i]
		if prev.Role == curr.Role && prev.Role != "system" {
			return fmt.Errorf("consecutive same role at messages %d-%d: %s (Claude requires alternating user/assistant)", i, i+1, curr.Role)
		}
	}

	return nil
}
