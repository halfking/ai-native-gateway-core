package compressor

import "testing"

// TestStringsToLower 验证 ASCII-only lowercase 实现.
//
// Known behaviour (audit pin): stringsToLower 只处理 ASCII A-Z. 不会处理
// Unicode (e.g. 'É' -> 'É' 不是 'é'). 这是性能优化 (避免 import strings),
// 不是 bug, 但需要 pin.
func TestStringsToLower(t *testing.T) {
tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty input", "", ""},
		{"all lower", "all lower", "all lower"},
		{"all upper", "all upper", "all upper"},
		{"mixed case", "MiXeD", "mixed"},
		{"with digits and symbols", "ABC123!@#", "abc123!@#"},
		// Known behaviour (audit pin): stringsToLower 是 ASCII-only 实现.
		// 多字节 UTF-8 序列里的非 A-Z ASCII byte 不动. 'ÉCLATÉ' 是
		// 'C3 89 43 4C 41 54 C3 89', 只有 43 4C 41 54 (C L A T) 在 A-Z
		// 范围. 所以转小写后 'C3 89 63 6C 61 74 C3 89' = 'ÉclatÉ'.
		// pin 真实行为:
		{"unicode ÉCLATÉ -> ÉclatÉ (only ASCII letters lowercase)", "ÉCLATÉ", "ÉclatÉ"},
		{"single char upper", "A", "a"},
		{"single char lower", "a", "a"},
		{"z boundary", "Z", "z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringsToLower(tt.in)
			if got != tt.want {
				t.Errorf("stringsToLower(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestStringsContains 验证 substring 检测.
func TestStringsContains(t *testing.T) {
	tests := []struct {
		s, substr string
		want      bool
	}{
		{"hello world", "world", true},
		{"hello world", "hello", true},
		{"hello world", "lo wo", true},
		{"hello world", "missing", false},
		{"hello", "", true},             // empty substring always matches
		{"", "anything", false},         // empty s, non-empty substr
		{"", "", true},                  // both empty
		{"abc", "abcdef", false},        // substr longer than s
		{"abc", "abc", true},            // exact match
		{"ABC", "abc", false},            // case-sensitive (stringsToLower 不联动)
		{"abc", "ABC", false},            // 反向 case-sensitive
	}
	for _, tt := range tests {
		name := tt.s + "/" + tt.substr
		t.Run(name, func(t *testing.T) {
			got := stringsContains(tt.s, tt.substr)
			if got != tt.want {
				t.Errorf("stringsContains(%q, %q) = %v, want %v",
					tt.s, tt.substr, got, tt.want)
			}
		})
	}
}

// TestDetectCompletionSignal 验证完成信号检测.
//
// Pins known behaviour: detects completion patterns (case-insensitive)
// and returns the matched pattern string, or empty.
func TestDetectCompletionSignal(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", ""},
		{"no signal", "what's the weather?", ""},
		{"done lowercase", "I'm done", "done"},
		{"DONE uppercase", "DONE with this", "done"}, // case-insensitive
		{"completed", "task completed", "completed"},
		// Pin: "thanks" 在 "thank you" 之前被匹配, 返回 "thanks"
		// (for-loop 找到第一个就 return). 如果改成反序会返回 "thank you".
		{"thanks!", "thanks!", "thanks"},
		{"next task", "next task: refactor", "next task"},
		{"that's all", "that's all for today", "that's all"},
		{"complex sentence", "I think we are done with phase 1, proceed to phase 2", "done"},
		{"signal in middle", "let's get done with this code review", "done"},
		{"unicode (no match)", "完成 done", "done"}, // ASCII 'done' still matches
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectCompletionSignal(tt.content)
			if got != tt.want {
				t.Errorf("detectCompletionSignal(%q) = %q, want %q",
					tt.content, got, tt.want)
			}
		})
	}
}

// TestDetectNewTaskStart 验证新任务起始检测.
func TestDetectNewTaskStart(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"no signal", "what's the weather?", false},
		{"new task", "new task: deploy", true},
		{"first,", "first, let me check the logs", true},
		{"next:", "next: optimize the database", true},
		{"also,", "also, can you check the metrics?", true},
		{"what about", "what about the tests?", true},
		{"normal question", "how do I deploy?", false},
		{"thanks (completion, not new task)", "thanks!", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectNewTaskStart(tt.content)
			if got != tt.want {
				t.Errorf("detectNewTaskStart(%q) = %v, want %v",
					tt.content, got, tt.want)
			}
		})
	}
}

// TestExtractTextContent_FromMap 验证消息 text 提取.
func TestExtractTextContent_FromMap(t *testing.T) {
	tests := []struct {
		name string
		msg  map[string]any
		want string
	}{
		{"no content key", map[string]any{"role": "user"}, ""},
		{"string content", map[string]any{"content": "hello"}, "hello"},
		{"empty content", map[string]any{"content": ""}, ""},
		{"empty parts array", map[string]any{"content": []any{}}, ""},
		{"single text part", map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "hello"}},
		}, "hello\n"},
		{"multiple text parts", map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "a"},
				map[string]any{"type": "text", "text": "b"},
			},
		}, "a\nb\n"},
		{"mixed text and image", map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "see:"},
				map[string]any{"type": "image", "src": "x"},
			},
		}, "see:\n"},
		{"non-string content (number)", map[string]any{"content": 42}, ""},
		{"nil content", map[string]any{"content": nil}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTextContent(tt.msg)
			if got != tt.want {
				t.Errorf("extractTextContent(%v) = %q, want %q",
					tt.msg, got, tt.want)
			}
		})
	}
}

// TestAnalyzeTasks_EmptyAndShort 验证边界输入.
//
// AnalyzeTasks 对空消息列表和单条消息应该返回 has_analysis=false (没足够
// 上下文分析). 这是设计: v4 compressor 只在 >= 2 条消息时跑 task 分析.
func TestAnalyzeTasks_EmptyAndShort(t *testing.T) {
	tests := []struct {
		name string
		msgs []map[string]any
	}{
		{"nil messages", nil},
		{"empty messages", []map[string]any{}},
		{"single message", []map[string]any{
			{"role": "user", "content": "hello"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AnalyzeTasks(tt.msgs)
			if result == nil {
				t.Fatal("AnalyzeTasks returned nil")
			}
			if result.HasAnalysis {
				t.Errorf("expected HasAnalysis=false for %s, got true (boundaries=%v)",
					tt.name, result.Boundaries)
			}
		})
	}
}

// TestAnalyzeTasks_CompleteWorkflow pins the fixed behaviour.
//
// Audit P1 fix (2026-06-22): boundary 由 completion signal 触发, 不是
// new_task signal. 修复后, msg[2] "thanks, done" (IsCompletion=true) 产生
// boundary msg[0-1] Completed, msg[3] "next task" (IsCompletion=false) 不
// 再覆盖前一个 completion. 同时 msg[3] 作为新 task 起点, 最终剩
// boundary msg[3-4] active.
func TestAnalyzeTasks_CompleteWorkflow(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "first task: write hello world"},
		{"role": "assistant", "content": "Here's the code: ..."},
		{"role": "user", "content": "thanks, done"},         // completion signal: "done"
		{"role": "user", "content": "next task: deploy it"}, // completion: "next task" + new_task
		{"role": "assistant", "content": "Deploying..."},
	}
	result := AnalyzeTasks(msgs)
	if result == nil {
		t.Fatal("AnalyzeTasks returned nil")
	}
	if !result.HasAnalysis {
		t.Error("expected HasAnalysis=true for >= 4 messages")
	}
	// Fix verification: CompletedCount=2 (msg[0-1] 完成 + msg[3-4] active 完成)
	// Wait, after fix: msg[2] "done" creates boundary 0-1 Completed=true.
	// msg[3] "next task" sets lastEnd=3 but doesn't create boundary.
	// msg[4] is last (len-1), so add active task msg[3-4].
	if result.CompletedCount != 1 {
		t.Errorf("audit pin (post-fix): expected CompletedCount=1 (msg[0-1] from done signal), got %d (boundaries=%+v)",
			result.CompletedCount, result.Boundaries)
	}
	if result.ActiveCount != 1 {
		t.Errorf("expected ActiveCount=1 (msg[3-4] from msg[3] new_task signal), got %d", result.ActiveCount)
	}
	if len(result.Boundaries) != 2 {
		t.Errorf("expected 2 boundaries (msg[0-1] completed + msg[3-4] active), got %d (boundaries=%+v)",
			len(result.Boundaries), result.Boundaries)
	}
}

// TestAnalyzeTasks_OnlyNewTask_NoCompletion 验证只有 new_task 信号时
// 不产生 phantom completed task.
//
// Known behaviour (audit pin): "first," 在 newTaskPatterns 里, 所以
// msg[0] "first, deploy" 产生 new_task signal (IsCompletion=false).
// msg[2] "next task: monitor" 也产生 new_task. 两个 new_task 之间
// 的 msg[1] (assistant) 形成 1 个 boundary, CompletedCount=1.
//
// 这是 task_analyzer 的逻辑: "first," 被当成 new_task 触发 (同时
// 也是 implicit completion 前一个 task 的标记). 不是 bug, 但语义模糊.
func TestAnalyzeTasks_OnlyNewTask_NoCompletion(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "first, deploy"},
		{"role": "assistant", "content": "deploying..."},
		{"role": "user", "content": "next task: monitor"},
		{"role": "assistant", "content": "monitoring..."},
	}
	result := AnalyzeTasks(msgs)
	if !result.HasAnalysis {
		t.Error("expected HasAnalysis=true")
	}
	// Pin: 1 boundary (msg[1-1] 完成 + msg[2-3] active), CompletedCount=1
	if result.CompletedCount != 1 {
		t.Errorf("pin: expected CompletedCount=1 (\"first,\" triggers new_task signal which marks msg[1] complete), got %d",
			result.CompletedCount)
	}
	if result.ActiveCount != 1 {
		t.Errorf("expected ActiveCount=1 (msg[2-3]), got %d", result.ActiveCount)
	}
}