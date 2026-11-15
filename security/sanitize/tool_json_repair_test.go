package sanitize

import (
	"encoding/json"
	"testing"
)

// TestTryRepairToolJSON_GLMNativeFormat 验证 GLM 原生标记泄漏的修补（rv-2089 形态）。
func TestTryRepairToolJSON_GLMNativeFormat(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantOK   bool
		validate func(t *testing.T, repaired string)
	}{
		{
			name:   "rv-2089 exact evidence",
			input:  `{"description</arg_key><ac7a3bd7><arg_value><b88a6f17>查找项目目录</arg_value><5b656597></tool_call><c4cf82b7>`,
			wantOK: true,
			validate: func(t *testing.T, repaired string) {
				// 修补后应该提取出纯文本并包装成 JSON
				if !json.Valid([]byte(repaired)) {
					t.Errorf("repaired JSON is invalid: %s", repaired)
				}
				// 应该包含原始文本
				var result map[string]any
				if err := json.Unmarshal([]byte(repaired), &result); err != nil {
					t.Fatalf("unmarshal failed: %v", err)
				}
				if val, ok := result["value"].(string); !ok || val == "" {
					t.Errorf("expected non-empty value field, got %v", result)
				}
			},
		},
		{
			name:   "GLM format with valid JSON inside",
			input:  `{"command</arg_key><tag1><arg_value>{"cmd": "ls -la"}</arg_value><tag2>`,
			wantOK: true,
			validate: func(t *testing.T, repaired string) {
				// 应该提取出内部的合法 JSON
				if !json.Valid([]byte(repaired)) {
					t.Errorf("repaired JSON is invalid: %s", repaired)
				}
				var result map[string]any
				if err := json.Unmarshal([]byte(repaired), &result); err != nil {
					t.Fatalf("unmarshal failed: %v", err)
				}
				if _, ok := result["cmd"]; !ok {
					t.Errorf("expected cmd field in repaired JSON: %s", repaired)
				}
			},
		},
		{
			name:   "GLM format with plain text",
			input:  `description</arg_key><hex><arg_value>Search project directory</arg_value><hex>`,
			wantOK: true,
			validate: func(t *testing.T, repaired string) {
				if !json.Valid([]byte(repaired)) {
					t.Errorf("repaired JSON is invalid: %s", repaired)
				}
			},
		},
		{
			name:   "truncated JSON gets repaired by subsequent logic",
			input:  `{"command": "ls`,
			wantOK: true, // 会被截断修补逻辑处理
			validate: func(t *testing.T, repaired string) {
				if !json.Valid([]byte(repaired)) {
					t.Errorf("repaired JSON is invalid: %s", repaired)
				}
			},
		},
	}
	
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repaired, ok := tryRepairToolJSON(tc.input, "TestTool")
			if ok != tc.wantOK {
				t.Errorf("tryRepairToolJSON() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && tc.validate != nil {
				tc.validate(t, repaired)
			}
			if ok && !json.Valid([]byte(repaired)) {
				t.Errorf("repaired JSON must be valid, got: %s", repaired)
			}
		})
	}
}

// TestTryRepairToolJSON_TruncatedJSON 验证 JSON 截断的修补（网络闪动）。
func TestTryRepairToolJSON_TruncatedJSON(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantOK   bool
		wantJSON string // 期望的修补结果
	}{
		{
			name:     "unclosed object",
			input:    `{"command": "ls -la"`,
			wantOK:   true,
			wantJSON: `{"command": "ls -la"}`,
		},
		{
			name:     "unclosed nested object",
			input:    `{"a": 1, "b": {"c": 2`,
			wantOK:   true,
			wantJSON: `{"a": 1, "b": {"c": 2}}`,
		},
		{
			name:     "unclosed array",
			input:    `{"items": [1, 2, 3`,
			wantOK:   true,
			wantJSON: `{"items": [1, 2, 3]}`,
		},
		{
			name:     "unclosed string only",
			input:    `{"message": "hello`,
			wantOK:   true,
			wantJSON: `{"message": "hello"}`,
		},
		{
			name:     "string + object unclosed",
			input:    `{"message": "hello`,
			wantOK:   true,
			wantJSON: `{"message": "hello"}`,
		},
		{
			name:   "already valid JSON",
			input:  `{"command": "ls -la"}`,
			wantOK: false, // 已经合法，不需要修补
		},
		{
			name:   "corrupted beyond repair",
			input:  `{"command": [[[}}}`,
			wantOK: false,
		},
	}
	
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repaired, ok := tryRepairToolJSON(tc.input, "TestTool")
			if ok != tc.wantOK {
				t.Errorf("tryRepairToolJSON() ok = %v, want %v (repaired: %s)", ok, tc.wantOK, repaired)
				return
			}
			if !ok {
				return
			}
			
			// 验证修补后的 JSON 合法
			if !json.Valid([]byte(repaired)) {
				t.Errorf("repaired JSON is invalid: %s", repaired)
			}
			
			// 验证修补结果符合预期（如果指定了）
			if tc.wantJSON != "" {
				// 标准化比较（忽略空格差异）
				var want, got any
				if err := json.Unmarshal([]byte(tc.wantJSON), &want); err != nil {
					t.Fatalf("invalid wantJSON: %v", err)
				}
				if err := json.Unmarshal([]byte(repaired), &got); err != nil {
					t.Fatalf("unmarshal repaired failed: %v", err)
				}
				
				wantBytes, _ := json.Marshal(want)
				gotBytes, _ := json.Marshal(got)
				if string(wantBytes) != string(gotBytes) {
					t.Errorf("repaired JSON mismatch:\nwant: %s\ngot:  %s", wantBytes, gotBytes)
				}
			}
		})
	}
}

// TestTryRepairToolJSON_EdgeCases 验证边缘情况。
func TestTryRepairToolJSON_EdgeCases(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		wantOK bool
	}{
		{
			name:   "empty string",
			input:  "",
			wantOK: false,
		},
		{
			name:   "single character",
			input:  "{",
			wantOK: true, // 可以补成 {}
		},
		{
			name:   "only whitespace",
			input:  "   ",
			wantOK: false,
		},
		{
			name:   "null",
			input:  "null",
			wantOK: false, // 已经合法
		},
		{
			name:   "number",
			input:  "123",
			wantOK: false, // 已经合法
		},
		{
			name:   "string literal",
			input:  `"hello"`,
			wantOK: false, // 已经合法
		},
		{
			name:   "GLM tags without content",
			input:  `</arg_key><arg_value></arg_value>`,
			wantOK: true, // 应该提取空字符串并包装
		},
		{
			name:   "incomplete GLM format (no arg_key tag)",
			input:  `<arg_value>first</arg_value><arg_value>second</arg_value>`,
			wantOK: false, // 缺少 </arg_key> 标记，不符合 GLM 完整格式
		},
	}
	
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repaired, ok := tryRepairToolJSON(tc.input, "TestTool")
			if ok != tc.wantOK {
				t.Errorf("tryRepairToolJSON() ok = %v, want %v (input: %q, repaired: %q)",
					ok, tc.wantOK, tc.input, repaired)
			}
			if ok && !json.Valid([]byte(repaired)) {
				t.Errorf("repaired JSON must be valid, got: %s", repaired)
			}
		})
	}
}

// TestTryRepairToolJSON_Integration 集成测试：验证修补器不会误伤合法 JSON。
func TestTryRepairToolJSON_Integration(t *testing.T) {
	validJSONs := []string{
		`{}`,
		`{"a": 1}`,
		`{"a": 1, "b": "hello"}`,
		`{"nested": {"a": 1}}`,
		`{"array": [1, 2, 3]}`,
		`{"complex": {"a": [1, {"b": 2}], "c": "d"}}`,
		`null`,
		`123`,
		`"string"`,
		`true`,
		`false`,
		`[]`,
		`[1, 2, 3]`,
	}
	
	for _, input := range validJSONs {
		t.Run(input, func(t *testing.T) {
			_, ok := tryRepairToolJSON(input, "TestTool")
			if ok {
				t.Errorf("tryRepairToolJSON() should not repair valid JSON: %s", input)
			}
		})
	}
}
