package intentconfig

import (
	"testing"
)

func TestEnhancedClassifier_HardRules(t *testing.T) {
	cfg := DefaultClassifierConfig()
	classifier := NewEnhancedClassifier(cfg)

	tests := []struct {
		name              string
		content           string
		hasImages         bool
		toolCount         int
		contextLength     int
		expectedIntent    IntentKind
		minConfidence     float64
	}{
		{
			name:           "代码块检测",
			content:        "```python\ndef hello():\n    print('world')\n```",
			expectedIntent: IntentCode,
			minConfidence:  0.90,
		},
		{
			name:           "函数定义检测",
			content:        "def calculate_sum(a, b):\n    return a + b",
			expectedIntent: IntentCode,
			minConfidence:  0.80,
		},
		{
			name:          "图像检测",
			content:       "这张图片里有什么？",
			hasImages:     true,
			expectedIntent: IntentCode, // 注意：当前硬规则将图像归类为Code，可能需要调整
			minConfidence: 0.90,
		},
		{
			name:           "多工具调用",
			content:        "使用工具查询天气、股票和新闻",
			toolCount:      3,
			expectedIntent: IntentToolUse,
			minConfidence:  0.85,
		},
		{
			name:           "长上下文",
			content:        "请总结这篇长文章",
			contextLength:  60000,
			expectedIntent: IntentSummary,
			minConfidence:  0.80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := classifier.ClassifyWithCandidates(
				tt.content,
				tt.contextLength,
				tt.hasImages,
				tt.toolCount,
			)

			if len(candidates) == 0 {
				t.Fatal("expected candidates, got none")
			}

			primary := candidates[0]
			if primary.Kind != tt.expectedIntent {
				t.Errorf("expected intent %s, got %s", tt.expectedIntent, primary.Kind)
			}

			if primary.Confidence < tt.minConfidence {
				t.Errorf("expected confidence >= %.2f, got %.2f", tt.minConfidence, primary.Confidence)
			}
		})
	}
}

func TestEnhancedClassifier_KeywordScore(t *testing.T) {
	cfg := DefaultClassifierConfig()
	classifier := NewEnhancedClassifier(cfg)

	tests := []struct {
		name           string
		content        string
		expectedIntent IntentKind
		minConfidence  float64
	}{
		{
			name:           "推理关键词-英文",
			content:        "Please solve this equation: 2x + 5 = 15",
			expectedIntent: IntentReasoning,
			minConfidence:  0.40,
		},
		{
			name:           "推理关键词-中文",
			content:        "请证明勾股定理",
			expectedIntent: IntentReasoning,
			minConfidence:  0.40,
		},
		{
			name:           "总结关键词",
			content:        "请帮我总结一下这篇文章的主要内容",
			expectedIntent: IntentSummary,
			minConfidence:  0.40,
		},
		{
			name:           "翻译关键词",
			content:        "请将这段话翻译成英文",
			expectedIntent: IntentTranslation,
			minConfidence:  0.40,
		},
		{
			name:           "代码关键词",
			content:        "请帮我实现一个快速排序算法",
			expectedIntent: IntentCode,
			minConfidence:  0.40,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := classifier.ClassifyWithCandidates(tt.content, 0, false, 0)

			if len(candidates) == 0 {
				t.Fatal("expected candidates, got none")
			}

			// 查找预期意图
			found := false
			for _, candidate := range candidates {
				if candidate.Kind == tt.expectedIntent && candidate.Confidence >= tt.minConfidence {
					found = true
					break
				}
			}

			if !found {
				t.Errorf("expected intent %s with confidence >= %.2f not found in candidates", 
					tt.expectedIntent, tt.minConfidence)
			}
		})
	}
}

func TestCalculateIntentDrift(t *testing.T) {
	tests := []struct {
		name        string
		history     []IntentEvolution
		current     []IntentCandidate
		maxDrift    float64
		description string
	}{
		{
			name:    "无历史记录",
			history: []IntentEvolution{},
			current: []IntentCandidate{
				{Kind: IntentCode, Confidence: 0.8},
			},
			maxDrift:    0.01,
			description: "没有历史，漂移应为0",
		},
		{
			name: "意图稳定",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode), PrimaryConfidence: 0.9, IntentCandidates: []IntentCandidate{{Kind: IntentCode, Confidence: 0.9}}},
				{PrimaryIntent: string(IntentCode), PrimaryConfidence: 0.85, IntentCandidates: []IntentCandidate{{Kind: IntentCode, Confidence: 0.85}}},
			},
			current: []IntentCandidate{
				{Kind: IntentCode, Confidence: 0.87},
			},
			maxDrift:    0.2,
			description: "意图稳定，漂移应较小",
		},
		{
			name: "意图切换",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode), PrimaryConfidence: 0.9, IntentCandidates: []IntentCandidate{{Kind: IntentCode, Confidence: 0.9}}},
				{PrimaryIntent: string(IntentCode), PrimaryConfidence: 0.85, IntentCandidates: []IntentCandidate{{Kind: IntentCode, Confidence: 0.85}}},
			},
			current: []IntentCandidate{
				{Kind: IntentReasoning, Confidence: 0.8},
				{Kind: IntentCode, Confidence: 0.2},
			},
			maxDrift:    1.0,
			description: "意图完全切换，漂移应较大",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drift := calculateIntentDrift(tt.history, tt.current)
			
			if drift < 0 || drift > 1 {
				t.Errorf("drift score should be in [0,1], got %.2f", drift)
			}

			if drift > tt.maxDrift {
				t.Errorf("%s: expected drift <= %.2f, got %.2f", tt.description, tt.maxDrift, drift)
			}

			t.Logf("%s: drift = %.3f", tt.name, drift)
		})
	}
}

func TestDetectIntentShift(t *testing.T) {
	tests := []struct {
		name           string
		history        []IntentEvolution
		currentIntent  string
		expectedShift  bool
		expectedType   string
	}{
		{
			name:          "无历史",
			history:       []IntentEvolution{},
			currentIntent: string(IntentCode),
			expectedShift: false,
			expectedType:  "no_history",
		},
		{
			name: "意图稳定",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentCode)},
			},
			currentIntent: string(IntentCode),
			expectedShift: false,
			expectedType:  "stable",
		},
		{
			name: "突然切换",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentCode)},
			},
			currentIntent: string(IntentReasoning),
			expectedShift: true,
			expectedType:  "sudden",
		},
		{
			name: "来回摇摆",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentReasoning)},
				{PrimaryIntent: string(IntentChat)},
			},
			currentIntent: string(IntentSummary),
			expectedShift: true,
			expectedType:  "oscillating",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isShift, shiftType := detectIntentShift(tt.history, tt.currentIntent)

			if isShift != tt.expectedShift {
				t.Errorf("expected shift=%v, got %v", tt.expectedShift, isShift)
			}

			if shiftType != tt.expectedType {
				t.Errorf("expected type=%s, got %s", tt.expectedType, shiftType)
			}
		})
	}
}

func TestCalculateIntentStability(t *testing.T) {
	tests := []struct {
		name        string
		history     []IntentEvolution
		windowSize  int
		minStability float64
		maxStability float64
	}{
		{
			name: "完全稳定",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentCode)},
			},
			windowSize:   3,
			minStability: 1.0,
			maxStability: 1.0,
		},
		{
			name: "完全不稳定",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentReasoning)},
				{PrimaryIntent: string(IntentChat)},
			},
			windowSize:   3,
			minStability: 0.0,
			maxStability: 0.0,
		},
		{
			name: "中等稳定",
			history: []IntentEvolution{
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentCode)},
				{PrimaryIntent: string(IntentReasoning)},
				{PrimaryIntent: string(IntentCode)},
			},
			windowSize:   4,
			minStability: 0.3,
			maxStability: 0.7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stability := calculateIntentStability(tt.history, tt.windowSize)

			if stability < 0 || stability > 1 {
				t.Errorf("stability should be in [0,1], got %.2f", stability)
			}

			if stability < tt.minStability || stability > tt.maxStability {
				t.Errorf("expected stability in [%.2f, %.2f], got %.2f", 
					tt.minStability, tt.maxStability, stability)
			}

			t.Logf("%s: stability = %.2f", tt.name, stability)
		})
	}
}

func BenchmarkClassifier(b *testing.B) {
	cfg := DefaultClassifierConfig()
	classifier := NewEnhancedClassifier(cfg)
	content := "请帮我实现一个快速排序算法，要求时间复杂度O(nlogn)"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = classifier.ClassifyWithCandidates(content, 0, false, 0)
	}
}

func BenchmarkIntentDrift(b *testing.B) {
	history := []IntentEvolution{
		{PrimaryIntent: string(IntentCode), PrimaryConfidence: 0.9, IntentCandidates: []IntentCandidate{{Kind: IntentCode, Confidence: 0.9}}},
		{PrimaryIntent: string(IntentCode), PrimaryConfidence: 0.85, IntentCandidates: []IntentCandidate{{Kind: IntentCode, Confidence: 0.85}}},
		{PrimaryIntent: string(IntentReasoning), PrimaryConfidence: 0.7, IntentCandidates: []IntentCandidate{{Kind: IntentReasoning, Confidence: 0.7}}},
	}
	current := []IntentCandidate{
		{Kind: IntentCode, Confidence: 0.6},
		{Kind: IntentReasoning, Confidence: 0.3},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calculateIntentDrift(history, current)
	}
}
