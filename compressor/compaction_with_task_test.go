// Package compressor - compaction_with_task_test.go (v3 T22)
//
// Tests that the new task-aware compaction prompt factory is wired through
// the v3 call chain (tryLLMContextCompactionWithTask).
package compressor

import (
	"strings"
	"testing"
)

func TestCompactionPromptForTaskType(t *testing.T) {
	tests := []struct {
		name     string
		taskType string
		contains string
	}{
		{
			name:     "default returns Chinese base",
			taskType: "",
			contains: "你是一个专业的对话历史压缩专家",
		},
		{
			name:     "code_debug returns EN",
			taskType: "code_debug",
			contains: "professional conversation compressor",
		},
		{
			name:     "data_analysis returns EN",
			taskType: "data_analysis",
			contains: "DATA tasks",
		},
		{
			name:     "doc_translate returns CN + suffix",
			taskType: "doc_translate",
			contains: "文档翻译任务",
		},
		{
			name:     "deployment returns CN + suffix",
			taskType: "deployment",
			contains: "部署/运维任务",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prompt := compactionPromptForTaskType(tc.taskType)
			if !strings.Contains(prompt, tc.contains) {
				t.Errorf("expected prompt to contain %q, got: %q", tc.contains, prompt[:min(120, len(prompt))])
			}
		})
	}
}

func TestBuildUserSummaryInstruction_LanguageMatch(t *testing.T) {
	// code_* tasks → English
	enInst := buildUserSummaryInstruction("code_debug", "...")
	if !strings.HasPrefix(enInst, "Summarize the following") {
		t.Errorf("expected English user instruction for code_*, got: %q", enInst[:min(60, len(enInst))])
	}

	// data_* tasks → English
	dataInst := buildUserSummaryInstruction("data_analysis", "...")
	if !strings.HasPrefix(dataInst, "Summarize the following") {
		t.Errorf("expected English user instruction for data_*, got: %q", dataInst[:min(60, len(dataInst))])
	}

	// default → Chinese (matches default CN system prompt)
	cnInst := buildUserSummaryInstruction("", "...")
	if !strings.HasPrefix(cnInst, "请压缩以下对话历史") {
		t.Errorf("expected Chinese user instruction for default, got: %q", cnInst[:min(60, len(cnInst))])
	}

	// doc_translate → Chinese (matches CN system prompt)
	cnInst2 := buildUserSummaryInstruction("doc_translate", "...")
	if !strings.HasPrefix(cnInst2, "请压缩以下对话历史") {
		t.Errorf("expected Chinese user instruction for doc_translate, got: %q", cnInst2[:min(60, len(cnInst2))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}