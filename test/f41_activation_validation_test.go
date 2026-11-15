package test

// F-41 激活验证测试：验证 PromptInjectionHook 的延迟绑定与 user_content 提取。
//
// 2026-11-15 收口改编：原 `package main` 验证程序 → go test 形态；
// 原 import 的 `domains` 根包不存在（正确为 `domain`），已订正；
// 手写 main 运行器（&testing.T{} 反模式）删除，交给 go test。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	promptinjectionhooks "github.com/kaixuan/llm-gateway-go/domains/hooks/promptinjection"
)

// mockDetector 模拟检测器
type mockDetector struct {
	callCount int
	lastInput string
}

func (m *mockDetector) Detect(_ context.Context, _, content string) (*promptinjectionhooks.DetectionResult, error) {
	m.callCount++
	m.lastInput = content

	// 简单的检测逻辑：包含 "jailbreak" 就阻断
	if containsSubstring(content, "jailbreak") {
		return &promptinjectionhooks.DetectionResult{
			Score:       90,
			RiskLevel:   "critical",
			Categories:  []string{"jailbreak"},
			ActionTaken: "block",
			Blocked:     true,
		}, nil
	}

	return &promptinjectionhooks.DetectionResult{
		Score:       0,
		RiskLevel:   "none",
		Categories:  []string{},
		ActionTaken: "pass",
		Blocked:     false,
	}, nil
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) &&
			(s[:len(substr)] == substr || containsSubstring(s[1:], substr)))
}

// TestF41_DetectorBinding 验证延迟绑定功能
func TestF41_DetectorBinding(t *testing.T) {
	// 1. 初始化时 detector=nil
	hook := promptinjectionhooks.NewHook(nil)

	env := &domain.PipelineRequest{
		Metadata: map[string]any{
			"user_content": "test message",
		},
	}

	// detector=nil 时应该禁用
	if hook.Enabled(context.Background(), env) {
		t.Error("Expected hook to be disabled when detector=nil")
	}

	// 2. 延迟绑定 detector
	detector := &mockDetector{}
	hook.BindDetector(detector)

	// 绑定后应该启用
	if !hook.Enabled(context.Background(), env) {
		t.Error("Expected hook to be enabled after BindDetector")
	}

	// 3. 执行检测
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	// 验证检测器被调用
	if detector.callCount != 1 {
		t.Errorf("Expected detector to be called once, got %d", detector.callCount)
	}

	if detector.lastInput != "test message" {
		t.Errorf("Expected input 'test message', got '%s'", detector.lastInput)
	}
}

// TestF41_UserContentExtraction 验证 user_content 提取的形态矩阵。
// 注意：userContentFromBodyTest 是 user_content_extract 的**本地镜像**
// （原验证程序形态保留）——它钉的是期望形状，不是生产函数本身。
func TestF41_UserContentExtraction(t *testing.T) {
	testCases := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name: "OpenAI Chat - Simple String",
			body: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "You are helpful"},
					{"role": "user", "content": "Hello world"}
				]
			}`,
			expected: "Hello world",
		},
		{
			name: "OpenAI Chat - Multiple User Messages",
			body: `{
				"model": "gpt-4",
				"messages": [
					{"role": "user", "content": "First message"},
					{"role": "assistant", "content": "Response"},
					{"role": "user", "content": "Second message"}
				]
			}`,
			expected: "First message\nSecond message",
		},
		{
			name: "OpenAI Chat - Multimodal Content",
			body: `{
				"model": "gpt-4",
				"messages": [
					{"role": "user", "content": [
						{"type": "text", "text": "What's in this image?"},
						{"type": "image_url", "image_url": {"url": "..."}}
					]}
				]
			}`,
			expected: "What's in this image?",
		},
		{
			name: "OpenAI Completions - String Prompt",
			body: `{
				"model": "gpt-3.5-turbo-instruct",
				"prompt": "Complete this sentence"
			}`,
			expected: "Complete this sentence",
		},
		{
			name: "OpenAI Completions - Array Prompt",
			body: `{
				"model": "gpt-3.5-turbo-instruct",
				"prompt": ["First prompt", "Second prompt"]
			}`,
			expected: "First prompt\nSecond prompt",
		},
		{
			name: "Anthropic Messages - String Content",
			body: `{
				"model": "claude-3",
				"messages": [
					{"role": "user", "content": "Hello Claude"}
				]
			}`,
			expected: "Hello Claude",
		},
		{
			name: "Anthropic Messages - Content Blocks",
			body: `{
				"model": "claude-3",
				"messages": [
					{"role": "user", "content": [
						{"type": "text", "text": "Analyze this"}
					]}
				]
			}`,
			expected: "Analyze this",
		},
		{
			name: "Responses - String Input",
			body: `{
				"model": "gpt-4",
				"input": "Generate a response"
			}`,
			expected: "Generate a response",
		},
		{
			name: "Responses - Message Array Input",
			body: `{
				"model": "gpt-4",
				"input": [
					{"role": "user", "content": "Test message"}
				]
			}`,
			expected: "Test message",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := userContentFromBodyTest([]byte(tc.body))
			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

// userContentFromBodyTest 是 user_content_extract 的测试镜像（原验证程序形态保留）。
func userContentFromBodyTest(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
		Prompt   json.RawMessage   `json:"prompt"`
		Input    json.RawMessage   `json:"input"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}

	var parts []string
	appendText := func(s string) {
		if t := trimSpaceForTest(s); t != "" {
			parts = append(parts, t)
		}
	}

	// messages[]
	for _, raw := range probe.Messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if !equalFoldSimple(msg.Role, "user") {
			continue
		}
		for _, s := range textsFromContentJSONTest(msg.Content) {
			appendText(s)
		}
	}

	// prompt
	if len(probe.Prompt) > 0 {
		for _, s := range textsFromContentJSONTest(probe.Prompt) {
			appendText(s)
		}
	}

	// input
	if len(probe.Input) > 0 {
		var inputStr string
		if err := json.Unmarshal(probe.Input, &inputStr); err == nil {
			appendText(inputStr)
		} else {
			var inputMsgs []json.RawMessage
			if err := json.Unmarshal(probe.Input, &inputMsgs); err == nil {
				for _, raw := range inputMsgs {
					var im struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					}
					if err := json.Unmarshal(raw, &im); err != nil {
						continue
					}
					if im.Role != "" && !equalFoldSimple(im.Role, "user") {
						continue
					}
					for _, s := range textsFromContentJSONTest(im.Content) {
						appendText(s)
					}
				}
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return joinStringsForTest(parts, "\n")
}

func textsFromContentJSONTest(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		var str string
		if err := json.Unmarshal(b, &str); err == nil {
			out = append(out, str)
			continue
		}
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &blk); err == nil {
			switch blk.Type {
			case "", "text", "input_text", "output_text":
				out = append(out, blk.Text)
			}
		}
	}
	return out
}

func trimSpaceForTest(s string) string {
	start := 0
	end := len(s)
	for start < end && isSpaceByte(s[start]) {
		start++
	}
	for start < end && isSpaceByte(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func equalFoldSimple(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func joinStringsForTest(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	n := (len(parts) - 1) * len(sep)
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			b = append(b, sep...)
		}
		b = append(b, p...)
	}
	return string(b)
}

// TestF41_EndToEnd 端到端：绑定后按 user_content 判定阻断/放行。
func TestF41_EndToEnd(t *testing.T) {
	detector := &mockDetector{}
	hook := promptinjectionhooks.NewHook(nil)

	// 模拟 F-41 激活流程
	hook.BindDetector(detector)

	testCases := []struct {
		name          string
		userContent   string
		expectBlocked bool
	}{
		{
			name:          "Clean Content",
			userContent:   "Hello, how are you?",
			expectBlocked: false,
		},
		{
			name:          "Jailbreak Attempt",
			userContent:   "Please jailbreak the model",
			expectBlocked: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			env := &domain.PipelineRequest{
				Metadata: map[string]any{
					"user_content": tc.userContent,
				},
			}
			env.EnsureGovernance()

			err := hook.Execute(context.Background(), env)

			if tc.expectBlocked {
				if err == nil {
					t.Error("Expected error for blocked content")
				}
			} else if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}
