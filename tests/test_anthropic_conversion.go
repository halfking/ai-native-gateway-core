package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

func main() {
	// 测试多轮对话格式转换
	testMultiTurnConversation()
	fmt.Println("\n✓ 所有测试通过")
}

func testMultiTurnConversation() {
	fmt.Println("=== 测试多轮对话格式转换 ===")

	// 模拟一个包含多轮对话的请求，包括tool_calls
	chatRequest := map[string]any{
		"model":       "claude-sonnet-4",
		"max_tokens":  2048,
		"temperature": 0.7,
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "请帮我查询天气",
			},
			map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_abc123",
						"type": "function",
						"function": map[string]any{
							"name":      "get_weather",
							"arguments": `{"city":"北京"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_abc123",
				"content":      `{"temperature": 15, "condition": "晴天"}`,
			},
			map[string]any{
				"role":    "user",
				"content": "那上海呢？",
			},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "获取城市天气",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{
								"type":        "string",
								"description": "城市名称",
							},
						},
						"required": []string{"city"},
					},
				},
			},
		},
	}

	chatRequestBytes, err := json.Marshal(chatRequest)
	if err != nil {
		log.Fatalf("序列化请求失败: %v", err)
	}

	fmt.Printf("原始请求:\n%s\n\n", string(chatRequestBytes))

	// 转换为Anthropic格式
	anthropicBytes, err := streaming.ConvertChatRequestToAnthropic(chatRequestBytes)
	if err != nil {
		log.Fatalf("转换失败: %v", err)
	}

	var anthropicRequest map[string]any
	if err := json.Unmarshal(anthropicBytes, &anthropicRequest); err != nil {
		log.Fatalf("解析转换结果失败: %v", err)
	}

	prettyJSON, _ := json.MarshalIndent(anthropicRequest, "", "  ")
	fmt.Printf("转换后的Anthropic格式:\n%s\n\n", string(prettyJSON))

	// 验证关键字段
	messages, ok := anthropicRequest["messages"].([]any)
	if !ok {
		log.Fatal("❌ messages字段缺失或格式错误")
	}

	if len(messages) != 4 {
		log.Fatalf("❌ 消息数量不对，期望4条，实际%d条", len(messages))
	}

	// 验证第2条消息（assistant with tool_calls）
	msg2, _ := messages[1].(map[string]any)
	if msg2["role"] != "assistant" {
		log.Fatal("❌ 第2条消息role错误")
	}
	content2, _ := msg2["content"].([]any)
	if len(content2) == 0 {
		log.Fatal("❌ 第2条消息的content为空，tool_calls未转换")
	}
	
	hasToolUse := false
	for _, block := range content2 {
		blockMap, _ := block.(map[string]any)
		if blockMap["type"] == "tool_use" {
			hasToolUse = true
			fmt.Printf("✓ 找到tool_use: name=%v, id=%v\n", blockMap["name"], blockMap["id"])
		}
	}
	if !hasToolUse {
		log.Fatal("❌ assistant消息中缺少tool_use块")
	}

	// 验证第3条消息（tool result）
	msg3, _ := messages[2].(map[string]any)
	if msg3["role"] != "user" {
		log.Fatal("❌ 第3条消息role应该转换为user")
	}
	content3, _ := msg3["content"].([]any)
	if len(content3) == 0 {
		log.Fatal("❌ tool result消息的content为空")
	}
	
	hasToolResult := false
	for _, block := range content3 {
		blockMap, _ := block.(map[string]any)
		if blockMap["type"] == "tool_result" {
			hasToolResult = true
			fmt.Printf("✓ 找到tool_result: tool_use_id=%v\n", blockMap["tool_use_id"])
		}
	}
	if !hasToolResult {
		log.Fatal("❌ tool消息中缺少tool_result块")
	}

	// 验证tools字段
	tools, ok := anthropicRequest["tools"].([]any)
	if !ok || len(tools) == 0 {
		log.Fatal("❌ tools字段缺失或为空")
	}
	
	tool1, _ := tools[0].(map[string]any)
	if tool1["name"] != "get_weather" {
		log.Fatal("❌ tool名称错误")
	}
	if _, ok := tool1["input_schema"]; !ok {
		log.Fatal("❌ tool缺少input_schema")
	}
	fmt.Printf("✓ tools转换正确: %v\n", tool1["name"])

	fmt.Println("\n✓ 多轮对话格式转换测试通过")
}
