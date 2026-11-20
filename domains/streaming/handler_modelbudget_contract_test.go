package streaming

import (
	"encoding/json"
	"testing"
)

// P1#4 Gateway 侧契约测试
// 验证 gateway 对 modelbudget 的基础集成契约

// TestChatRequestBody_MaxCostPerTask_Parsing 验证 max_cost_per_task 字段解析
func TestChatRequestBody_MaxCostPerTask_Parsing(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		want    float64
		wantErr bool
	}{
		{
			name: "有预算约束",
			json: `{"model":"gpt-4","max_cost_per_task":1.5}`,
			want: 1.5,
		},
		{
			name: "无预算字段（向后兼容）",
			json: `{"model":"gpt-4"}`,
			want: 0, // 默认值
		},
		{
			name: "预算为零（禁用）",
			json: `{"model":"gpt-4","max_cost_per_task":0}`,
			want: 0,
		},
		{
			name: "极小预算",
			json: `{"model":"gpt-4","max_cost_per_task":0.001}`,
			want: 0.001,
		},
		{
			name: "负预算（应解析但语义上无效）",
			json: `{"model":"gpt-4","max_cost_per_task":-1.0}`,
			want: -1.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body chatRequestBody
			err := json.Unmarshal([]byte(tc.json), &body)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Unmarshal() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if body.MaxCostPerTask != tc.want {
				t.Errorf("MaxCostPerTask = %v, want %v", body.MaxCostPerTask, tc.want)
			}
		})
	}
}

// TestEstimateInputTokensFromMessages 验证 token 估算函数
func TestEstimateInputTokensFromMessages(t *testing.T) {
	cases := []struct {
		name     string
		messages json.RawMessage
		wantMin  int // 最小期望值
		wantMax  int // 最大期望值（允许范围）
	}{
		{
			name:     "空消息",
			messages: json.RawMessage(`[]`),
			wantMin:  10, // 最小值保护
			wantMax:  10,
		},
		{
			name:     "短消息",
			messages: json.RawMessage(`[{"role":"user","content":"hi"}]`),
			wantMin:  10, // 实际约 11 chars / 4 = 2.75, 但有最小值保护
			wantMax:  20,
		},
		{
			name:     "中等消息",
			messages: json.RawMessage(`[{"role":"user","content":"这是一个测试消息，包含一些中文字符和 English words."}]`),
			wantMin:  15,
			wantMax:  40,
		},
		{
			name:     "长消息（~400字符）",
			messages: json.RawMessage(`[{"role":"user","content":"` + makeString(400) + `"}]`),
			wantMin:  100,
			wantMax:  150,
		},
		{
			name:     "多条消息",
			messages: json.RawMessage(`[{"role":"user","content":"hello"},{"role":"assistant","content":"hi there"},{"role":"user","content":"how are you?"}]`),
			wantMin:  25, // 实际约 115 chars / 4 ≈ 29 tokens
			wantMax:  35,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := estimateInputTokensFromMessages(tc.messages)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("estimateInputTokensFromMessages() = %d, want range [%d, %d]", got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// TestEstimateInputTokensFromMessages_EdgeCases 边界情况
func TestEstimateInputTokensFromMessages_EdgeCases(t *testing.T) {
	t.Run("nil消息", func(t *testing.T) {
		got := estimateInputTokensFromMessages(nil)
		if got != 0 {
			t.Errorf("nil messages should return 0, got %d", got)
		}
	})

	t.Run("空字节", func(t *testing.T) {
		got := estimateInputTokensFromMessages(json.RawMessage{})
		if got != 0 {
			t.Errorf("empty bytes should return 0, got %d", got)
		}
	})

	t.Run("最小值保护", func(t *testing.T) {
		// 非常短的输入应该有最小值保护
		short := json.RawMessage(`[]`)
		got := estimateInputTokensFromMessages(short)
		if got < 10 {
			t.Errorf("minimum protection failed, got %d < 10", got)
		}
	})
}

// makeString 生成指定长度的测试字符串
func makeString(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
