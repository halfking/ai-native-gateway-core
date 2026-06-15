package autoroute

import (
	"context"
	"testing"
)

// TestPatternLayer_WaterPoolProblem tests the canonical failure case that
// motivated the regex pattern layer: the "水池问题" (water-pool problem).
//
// Before Phase 1, this was classified as TaskChat (confidence 0.1) because
// the text contains no reasoning keyword ("求解"/"推导"/"计算") but is
// unmistakably a multi-step math task via the structural pattern
// "每(分钟)...(多少)".
func TestPatternLayer_WaterPoolProblem(t *testing.T) {
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	tests := []struct {
		name    string
		prompt  string
		want    TaskType
		minConf float64
	}{
		{
			name:    "水池问题_经典",
			prompt:  "有一个水池，进水管每分钟进水10升，排水管每分钟排水8升。如果水池原本有20升水，需要多少分钟才能装满80升的水池？",
			want:    TaskReasoning,
			minConf: 0.60, // pattern weight 0.65
		},
		{
			name:    "水池问题_每小时",
			prompt:  "工厂每小时生产100个零件，质检每小时淘汰5个。需要多少小时才能积攒到1000个合格品？",
			want:    TaskReasoning,
			minConf: 0.60,
		},
		{
			name:    "水池问题_每天",
			prompt:  "存款每天利息10元，手续费每天扣3元，几天能攒够500元？",
			want:    TaskReasoning,
			minConf: 0.60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, err := clf.Classify(context.Background(), ClassificationSignals{
				LastUserPrompt: tt.prompt,
				Language:       "zh",
			})
			if err != nil {
				t.Fatalf("Classify error: %v", err)
			}
			if cls.Primary != tt.want {
				t.Errorf("task = %s, want %s (reason: %s)", cls.Primary, tt.want, cls.Reason)
			}
			if cls.Confidence < tt.minConf {
				t.Errorf("confidence = %.2f, want >= %.2f", cls.Confidence, tt.minConf)
			}
		})
	}
}

// TestPatternLayer_CodeDefinitionSyntax verifies that function/class
// definition syntax is detected via patterns even when no code keyword
// ("代码"/"函数") appears in the prompt.
func TestPatternLayer_CodeDefinitionSyntax(t *testing.T) {
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	tests := []struct {
		name    string
		prompt  string
		want    TaskType
		minConf float64
	}{
		{
			name:    "Python_def",
			prompt:  "请帮我把这个 def calculate_sum(a, b): return a + b 改成支持多个参数",
			want:    TaskCode,
			minConf: 0.60,
		},
		{
			name:    "Go_func",
			prompt:  "这里有个 func ProcessData(input []byte) error 怎么优化",
			want:    TaskCode,
			minConf: 0.60,
		},
		{
			name:    "Java_class",
			prompt:  "我想理解这个 class UserService { } 的设计思路",
			want:    TaskCode,
			minConf: 0.60,
		},
		{
			name:    "import语句",
			prompt:  "报错了，import React, { useState } from 'react' 这行有什么问题",
			want:    TaskCode,
			minConf: 0.50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, err := clf.Classify(context.Background(), ClassificationSignals{
				LastUserPrompt: tt.prompt,
				Language:       "mixed",
			})
			if err != nil {
				t.Fatalf("Classify error: %v", err)
			}
			if cls.Primary != tt.want {
				t.Errorf("task = %s, want %s (reason: %s)", cls.Primary, tt.want, cls.Reason)
			}
			if cls.Confidence < tt.minConf {
				t.Errorf("confidence = %.2f, want >= %.2f", cls.Confidence, tt.minConf)
			}
		})
	}
}

// TestPatternLayer_StatsAndOptimization verifies reasoning patterns for
// statistics and optimization phrasing.
func TestPatternLayer_StatsAndOptimization(t *testing.T) {
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	tests := []struct {
		name   string
		prompt string
		want   TaskType
	}{
		{
			name:   "概率统计",
			prompt: "掷两个骰子，概率是多少能拿到大于10的点数",
			want:   TaskReasoning,
		},
		{
			name:   "排列组合",
			prompt: "5个人排成一排有多少种排列方式",
			want:   TaskReasoning,
		},
		{
			name:   "求最大值",
			prompt: "给定一个数组，求最大值并返回其索引",
			want:   TaskReasoning,
		},
		{
			name:   "贝叶斯",
			prompt: "用贝叶斯方法分析这个分类问题",
			want:   TaskReasoning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, err := clf.Classify(context.Background(), ClassificationSignals{
				LastUserPrompt: tt.prompt,
				Language:       "zh",
			})
			if err != nil {
				t.Fatalf("Classify error: %v", err)
			}
			if cls.Primary != tt.want {
				t.Errorf("task = %s, want %s (reason: %s)", cls.Primary, tt.want, cls.Reason)
			}
		})
	}
}

// TestPatternLayer_CreativeWriting verifies creative-writing patterns.
func TestPatternLayer_CreativeWriting(t *testing.T) {
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	tests := []struct {
		name string
		prompt string
		want TaskType
	}{
		{
			name: "写一首诗",
			prompt: "帮我写一首关于秋天的诗",
			want:   TaskCreative,
		},
		{
			name: "写一个故事",
			prompt: "给我写一个关于冒险的故事",
			want:   TaskCreative,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, err := clf.Classify(context.Background(), ClassificationSignals{
				LastUserPrompt: tt.prompt,
				Language:       "zh",
			})
			if err != nil {
				t.Fatalf("Classify error: %v", err)
			}
			if cls.Primary != tt.want {
				t.Errorf("task = %s, want %s (reason: %s)", cls.Primary, tt.want, cls.Reason)
			}
		})
	}
}

// TestPatternLayer_DoesNotFalsePositive ensures patterns don't over-trigger
// on benign prompts that happen to contain a number or a common word.
func TestPatternLayer_DoesNotFalsePositive(t *testing.T) {
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	tests := []struct {
		name   string
		prompt string
		want   TaskType
	}{
		{
			name:   "普通数字不触发推理",
			prompt: "我今年25岁了，在北京工作",
			want:   TaskChat,
		},
		{
			name:   "日常对话不含模式",
			prompt: "你好，请问你叫什么名字？",
			want:   TaskChat,
		},
		{
			name:   "提及分钟但非应用题",
			prompt: "我等了三分钟就放弃了",
			want:   TaskChat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, err := clf.Classify(context.Background(), ClassificationSignals{
				LastUserPrompt: tt.prompt,
				Language:       "zh",
			})
			if err != nil {
				t.Fatalf("Classify error: %v", err)
			}
			if cls.Primary != tt.want {
				t.Errorf("task = %s, want %s (reason: %s) — false positive!", cls.Primary, tt.want, cls.Reason)
			}
		})
	}
}

// TestPatternLayer_MaxMergeWithKeywords verifies that when both a pattern
// and a keyword fire for the same task type, the higher score wins
// (max-merge semantics).
func TestPatternLayer_MaxMergeWithKeywords(t *testing.T) {
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	// "求解" is a reasoning keyword (0.4 weight) + the prompt also matches
	// the arithmetic pattern "3 + 5 = " (0.55). The keyword + pattern should
	// merge to at least 0.55, and since "求解" fires we expect reasoning.
	cls, err := clf.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "求解 3 + 5 = ? 这个算式",
		Language:       "zh",
	})
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if cls.Primary != TaskReasoning {
		t.Errorf("task = %s, want TaskReasoning", cls.Primary)
	}
	// Keyword "求解" gives 0.4, arithmetic pattern gives 0.55 → max = 0.55
	if cls.Confidence < 0.50 {
		t.Errorf("confidence = %.2f, expected >= 0.50 (keyword+pattern merge)", cls.Confidence)
	}
	// Reason should mention the pattern contribution
	t.Logf("reason: %s, confidence: %.2f", cls.Reason, cls.Confidence)
}

// TestMatchPatterns_Direct tests the matchPatterns helper directly.
func TestMatchPatterns_Direct(t *testing.T) {
	tests := []struct {
		text     string
		wantTask TaskType
	}{
		{"每分钟进水10升需要多少分钟", TaskReasoning},
		{"def foo(): pass", TaskCode},
		{"概率是多少", TaskReasoning},
		{"写一首诗", TaskCreative},
	}

	for _, tt := range tests {
		hits := matchPatterns(tt.text)
		if _, ok := hits[tt.wantTask]; !ok {
			t.Errorf("matchPatterns(%q): no match for %s, got %d hits", tt.text, tt.wantTask, len(hits))
		}
	}

	// No patterns should match plain text
	hits := matchPatterns("你好世界")
	if len(hits) != 0 {
		t.Errorf("matchPatterns on benign text returned %d hits (expected 0)", len(hits))
	}
}

// TestDefaultPatterns verifies the pattern registry is non-empty and
// every pattern compiles successfully (the init() would panic otherwise,
// but this makes the test explicit).
func TestDefaultPatterns(t *testing.T) {
	patterns := DefaultPatterns()
	if len(patterns) < 5 {
		t.Errorf("DefaultPatterns returned %d patterns, expected >= 5", len(patterns))
	}
	seenTasks := make(map[TaskType]bool)
	for _, p := range patterns {
		seenTasks[p.TaskType] = true
		if p.Weight < 0.5 || p.Weight > 0.7 {
			t.Errorf("pattern weight %.2f out of [0.5, 0.7] range: %s", p.Weight, p.Reason)
		}
		if p.Reason == "" {
			t.Error("pattern has empty reason")
		}
	}
	// Should cover at least reasoning, code, and creative
	for _, task := range []TaskType{TaskReasoning, TaskCode, TaskCreative} {
		if !seenTasks[task] {
			t.Errorf("DefaultPatterns missing coverage for %s", task)
		}
	}
}
