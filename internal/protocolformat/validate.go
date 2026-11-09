// Package protocolformat (2026-11-10) — 协议格式门（用户规则）：
//
//	客户端请求协议 == 网关出口协议时，请求体直传（不改参数/内容格式），
//	但必须先过格式检查——格式无错误才放行。
//
// 设计：按各协议官方文档的最小必要形状校验（fail-closed 只拒「协议层面
// 不合法」的体，不做网关方言级别的强校验——直传语义要求网关不替上游做
// 裁决）。跨协议路径不走本包：转换器（IR parse/serialize）本身就是更严
// 的格式检查。
package protocolformat

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// anthropicBlockTypes 是 Anthropic Messages API 请求侧合法内容块闭集
// （docs.anthropic.com Messages API reference）。
var anthropicBlockTypes = map[string]bool{
	"text": true, "image": true, "document": true,
	"tool_use": true, "tool_result": true,
	"thinking": true, "redacted_thinking": true,
	"server_tool_use": true, "web_search_tool_result": true,
	"search_result": true, "container_upload": true, "citations": true,
}

// openaiChatRoles 是 OpenAI Chat Completions 请求侧合法角色闭集。
var openaiChatRoles = map[string]bool{
	"system": true, "developer": true, "user": true,
	"assistant": true, "tool": true, "function": true,
}

// ValidateAnthropicRequest checks the minimal wire shape required by the
// Anthropic Messages API: a JSON object carrying model, max_tokens and a
// non-empty messages array whose roles and content-block types are in the
// official closure, with tool_use/tool_result carrying their required ids.
func ValidateAnthropicRequest(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root map[string]json.RawMessage
	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("body is not a JSON object: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("body carries trailing JSON values")
	}
	if string(root["model"]) == "" {
		return fmt.Errorf("model is required")
	}
	var model string
	if err := json.Unmarshal(root["model"], &model); err != nil || model == "" {
		return fmt.Errorf("model must be a non-empty string")
	}
	if root["max_tokens"] == nil {
		return fmt.Errorf("max_tokens is required")
	}
	var maxTokens json.Number
	if err := json.Unmarshal(root["max_tokens"], &maxTokens); err != nil {
		return fmt.Errorf("max_tokens must be a number")
	}
	messagesRaw, ok := root["messages"]
	if !ok {
		return fmt.Errorf("messages is required")
	}
	var messages []map[string]json.RawMessage
	// messages 允许空数组（官方要求非空，但内部机制腿会构造退化体；空数组
	// 由上游自行拒绝——本门只拦协议层不合法的形状）。
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return fmt.Errorf("messages must be an array")
	}
	for i, msg := range messages {
		var role string
		if err := json.Unmarshal(msg["role"], &role); err != nil {
			return fmt.Errorf("messages[%d].role must be a string", i)
		}
		if role != "user" && role != "assistant" {
			return fmt.Errorf("messages[%d].role %q is not allowed (anthropic-messages accepts user|assistant)", i, role)
		}
		if err := validateAnthropicContent(i, role, msg["content"]); err != nil {
			return err
		}
	}
	return nil
}

func validateAnthropicContent(i int, role string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("messages[%d].content is required", i)
	}
	var contentStr string
	if err := json.Unmarshal(raw, &contentStr); err == nil {
		return nil // string content is legal for both roles
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return fmt.Errorf("messages[%d].content must be a string or a block array", i)
	}
	for j, blk := range blocks {
		t, _ := blk["type"].(string)
		if !anthropicBlockTypes[t] {
			return fmt.Errorf("messages[%d].content[%d].type %q is not a legal anthropic-messages content block type", i, j, t)
		}
		switch t {
		case "tool_use":
			id, _ := blk["id"].(string)
			name, _ := blk["name"].(string)
			if id == "" || name == "" {
				return fmt.Errorf("messages[%d].content[%d] tool_use requires id and name", i, j)
			}
		case "tool_result":
			if role != "user" {
				return fmt.Errorf("messages[%d].content[%d] tool_result must live in a user message", i, j)
			}
			id, _ := blk["tool_use_id"].(string)
			if id == "" {
				return fmt.Errorf("messages[%d].content[%d] tool_result requires tool_use_id", i, j)
			}
		}
	}
	return nil
}

// ValidateOpenAIChatRequest checks the minimal wire shape required by the
// OpenAI Chat Completions API: a JSON object carrying model and a non-empty
// messages array with roles in the official closure; assistant tool_calls and
// tool results carry their required ids.
func ValidateOpenAIChatRequest(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root map[string]json.RawMessage
	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("body is not a JSON object: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("body carries trailing JSON values")
	}
	var model string
	if err := json.Unmarshal(root["model"], &model); err != nil || model == "" {
		return fmt.Errorf("model must be a non-empty string")
	}
	messagesRaw, ok := root["messages"]
	if !ok {
		return fmt.Errorf("messages is required")
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return fmt.Errorf("messages must be an array")
	}
	for i, msg := range messages {
		var role string
		if err := json.Unmarshal(msg["role"], &role); err != nil {
			return fmt.Errorf("messages[%d].role must be a string", i)
		}
		if !openaiChatRoles[role] {
			return fmt.Errorf("messages[%d].role %q is not allowed (openai-completions role closure violated)", i, role)
		}
		if role == "tool" {
			var callID string
			if err := json.Unmarshal(msg["tool_call_id"], &callID); err != nil || callID == "" {
				return fmt.Errorf("messages[%d] tool role requires tool_call_id", i)
			}
		}
		if role == "assistant" && msg["tool_calls"] != nil {
			var calls []map[string]any
			if err := json.Unmarshal(msg["tool_calls"], &calls); err != nil {
				return fmt.Errorf("messages[%d].tool_calls must be an array", i)
			}
			for j, call := range calls {
				fn, _ := call["function"].(map[string]any)
				name, _ := fn["name"].(string)
				if call["id"] == "" || name == "" {
					return fmt.Errorf("messages[%d].tool_calls[%d] requires id and function.name", i, j)
				}
			}
		}
	}
	return nil
}
