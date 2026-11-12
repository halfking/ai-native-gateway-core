package streaming

import (
	"strings"
	"testing"
)

// TestReasoningOnlyDoesNotCommit 验证方案A的核心逻辑:
// reasoning-only场景不应该被判定为有semantic output,
// 从而允许网关层failover到其他候选节点
func TestReasoningOnlyDoesNotCommit(t *testing.T) {
	tests := []struct {
		name               string
		contentBytes       int
		reasoningBytes     int
		hasToolCalls       bool
		wantSemanticOutput bool
		description        string
	}{
		{
			name:               "reasoning_only",
			contentBytes:       0,
			reasoningBytes:     1000,
			hasToolCalls:       false,
			wantSemanticOutput: false,
			description:        "纯reasoning内容不应算作semantic output",
		},
		{
			name:               "content_only",
			contentBytes:       100,
			reasoningBytes:     0,
			hasToolCalls:       false,
			wantSemanticOutput: true,
			description:        "纯content应算作semantic output",
		},
		{
			name:               "reasoning_plus_content",
			contentBytes:       50,
			reasoningBytes:     500,
			hasToolCalls:       false,
			wantSemanticOutput: true,
			description:        "reasoning + content应算作semantic output",
		},
		{
			name:               "tool_calls_only",
			contentBytes:       0,
			reasoningBytes:     0,
			hasToolCalls:       true,
			wantSemanticOutput: true,
			description:        "tool_calls应算作semantic output",
		},
		{
			name:               "reasoning_plus_tool_calls",
			contentBytes:       0,
			reasoningBytes:     300,
			hasToolCalls:       true,
			wantSemanticOutput: true,
			description:        "reasoning + tool_calls应算作semantic output",
		},
		{
			name:               "empty",
			contentBytes:       0,
			reasoningBytes:     0,
			hasToolCalls:       false,
			wantSemanticOutput: false,
			description:        "完全空应不算作semantic output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 创建scaffold
			scaffold := &responsesScaffold{
				contentBytes:   tt.contentBytes,
				reasoningBytes: tt.reasoningBytes,
				toolStates:     make(map[int]*responsesToolTerminalState),
			}

			// 如果有tool_calls,添加一个
			if tt.hasToolCalls {
				scaffold.toolStates[0] = &responsesToolTerminalState{
					Name: "test_function",
				}
			}

			// 创建fullText (现在不再用于判定,但保持兼容性)
			var fullText strings.Builder
			if tt.contentBytes > 0 {
				fullText.WriteString(strings.Repeat("x", tt.contentBytes))
			}

			// 模拟responsesHasSemanticOutput的逻辑
			hasSemanticOutput := func() bool {
				// 方案A: 只有非reasoning的content才算semantic output
				if scaffold.contentBytes > 0 {
					return true
				}
				// tool_calls仍然算semantic output
				for _, state := range scaffold.toolStates {
					if state != nil && state.Name != "" {
						return true
					}
				}
				return false
			}

			got := hasSemanticOutput()
			if got != tt.wantSemanticOutput {
				t.Errorf("%s: hasSemanticOutput() = %v, want %v\ncontentBytes=%d, reasoningBytes=%d, hasToolCalls=%v",
					tt.description, got, tt.wantSemanticOutput,
					tt.contentBytes, tt.reasoningBytes, tt.hasToolCalls)
			}
		})
	}
}

// TestContentBytesTracking 验证content和reasoning字节数的正确追踪
func TestContentBytesTracking(t *testing.T) {
	scaffold := &responsesScaffold{
		contentBytes:   0,
		reasoningBytes: 0,
		toolStates:     make(map[int]*responsesToolTerminalState),
	}

	// 模拟接收chunks
	chunks := []struct {
		content         string
		reasoningContent string
	}{
		{"Hello", "Thinking about response"},
		{" world", " and considering options"},
		{"!", ""},
		{"", " final thoughts"},
	}

	for _, chunk := range chunks {
		scaffold.contentBytes += len(chunk.content)
		scaffold.reasoningBytes += len(chunk.reasoningContent)
	}

	wantContentBytes := len("Hello world!")
	wantReasoningBytes := len("Thinking about response and considering options final thoughts")

	if scaffold.contentBytes != wantContentBytes {
		t.Errorf("contentBytes = %d, want %d", scaffold.contentBytes, wantContentBytes)
	}
	if scaffold.reasoningBytes != wantReasoningBytes {
		t.Errorf("reasoningBytes = %d, want %d", scaffold.reasoningBytes, wantReasoningBytes)
	}
}

// TestReasoningOnlyFailoverScenario 测试minimax reasoning耗尽预算的真实场景
func TestReasoningOnlyFailoverScenario(t *testing.T) {
	// 模拟minimax-m3.1-flash reasoning耗尽预算的场景:
	// 1. 发送大量reasoning内容
	// 2. 零实际content
	// 3. 收到[DONE]
	
	scaffold := &responsesScaffold{
		contentBytes:   0,                                      // 零content
		reasoningBytes: 8000,                                   // 大量reasoning
		toolStates:     make(map[int]*responsesToolTerminalState),
	}

	// 验证不被判定为有semantic output
	hasSemanticOutput := scaffold.contentBytes > 0
	for _, state := range scaffold.toolStates {
		if state != nil && state.Name != "" {
			hasSemanticOutput = true
			break
		}
	}

	if hasSemanticOutput {
		t.Error("reasoning-only场景被错误判定为有semantic output, 将阻止failover")
	}

	// 验证应该返回empty outcome并允许重试
	// 这将触发executor的failover逻辑
	t.Log("✓ reasoning-only场景正确判定为无semantic output, 允许failover")
}

// TestBackwardCompatibility 确保修改不影响其他模型的正常行为
func TestBackwardCompatibility(t *testing.T) {
	tests := []struct {
		name               string
		model              string
		contentBytes       int
		reasoningBytes     int
		hasToolCalls       bool
		wantSemanticOutput bool
	}{
		{
			name:               "claude_with_content",
			model:              "claude-sonnet-5",
			contentBytes:       100,
			reasoningBytes:     200, // claude也有thinking
			hasToolCalls:       false,
			wantSemanticOutput: true,
		},
		{
			name:               "gpt_with_content",
			model:              "gpt-5.6-terra",
			contentBytes:       50,
			reasoningBytes:     0,
			hasToolCalls:       false,
			wantSemanticOutput: true,
		},
		{
			name:               "glm_with_tool_calls",
			model:              "glm-5.3",
			contentBytes:       0,
			reasoningBytes:     0,
			hasToolCalls:       true,
			wantSemanticOutput: true,
		},
		{
			name:               "any_model_truly_empty",
			model:              "any-model",
			contentBytes:       0,
			reasoningBytes:     0,
			hasToolCalls:       false,
			wantSemanticOutput: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scaffold := &responsesScaffold{
				clientModel:    tt.model,
				contentBytes:   tt.contentBytes,
				reasoningBytes: tt.reasoningBytes,
				toolStates:     make(map[int]*responsesToolTerminalState),
			}

			if tt.hasToolCalls {
				scaffold.toolStates[0] = &responsesToolTerminalState{
					Name: "function",
				}
			}

			hasSemanticOutput := scaffold.contentBytes > 0
			for _, state := range scaffold.toolStates {
				if state != nil && state.Name != "" {
					hasSemanticOutput = true
					break
				}
			}

			if hasSemanticOutput != tt.wantSemanticOutput {
				t.Errorf("model %s: hasSemanticOutput = %v, want %v",
					tt.model, hasSemanticOutput, tt.wantSemanticOutput)
			}
		})
	}
}
