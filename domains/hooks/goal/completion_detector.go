package goal

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// CompletionDetector detects task completion using multiple strategies.
// A single detector is shared across all goal-mode requests (it lives on
// ModeHook, a singleton on ChatHandler). minConfidenceBits holds the
// configured LLM-verdict threshold as its IEEE-754 bit pattern in an
// atomic.Uint64 so SetMinConfidence (per-tenant writes) and checkWithLLM
// (reads) are race-free under concurrent interception.
type CompletionDetector struct {
	db                GoalStore
	llmCaller         LLMCaller
	minConfidenceBits atomic.Uint64
}

// DefaultCompletionConfidence is the threshold used when a caller hasn't
// configured one. Matches the historical hard-coded value.
const DefaultCompletionConfidence = 0.8

// NewCompletionDetector creates a new completion detector. When minConfidence
// is 0, DefaultCompletionConfidence (0.8) is used.
func NewCompletionDetector(db GoalStore, llmCaller LLMCaller) *CompletionDetector {
	return NewCompletionDetectorWithConfidence(db, llmCaller, DefaultCompletionConfidence)
}

// NewCompletionDetectorWithConfidence lets the caller inject a per-tenant
// confidence threshold (read from settings by ModeHook).
func NewCompletionDetectorWithConfidence(db GoalStore, llmCaller LLMCaller, minConfidence float64) *CompletionDetector {
	if minConfidence <= 0 {
		minConfidence = DefaultCompletionConfidence
	}
	d := &CompletionDetector{db: db, llmCaller: llmCaller}
	d.minConfidenceBits.Store(math.Float64bits(minConfidence))
	return d
}

// SetMinConfidence updates the LLM-verdict confidence threshold. ModeHook calls
// this per interception with the tenant's configured value so the threshold is
// hot-reloadable. Values <= 0 reset to DefaultCompletionConfidence.
func (d *CompletionDetector) SetMinConfidence(min float64) {
	if min <= 0 {
		min = DefaultCompletionConfidence
	}
	d.minConfidenceBits.Store(math.Float64bits(min))
}

// minConfidence returns the currently-effective threshold (race-free read).
func (d *CompletionDetector) minConfidence() float64 {
	return math.Float64frombits(d.minConfidenceBits.Load())
}

// IsCompleted checks if the task is completed using hybrid detection.
func (d *CompletionDetector) IsCompleted(ctx context.Context, req *response.InterceptRequest) (bool, float64, string) {
	// Strategy 1: Check structured output
	if completed, confidence, reason := d.checkStructuredOutput(req); completed {
		return true, confidence, "structured:" + reason
	}

	// Strategy 2: Check keywords
	if completed, confidence, reason := d.checkKeywords(req); completed {
		return true, confidence, "keyword:" + reason
	}

	// Strategy 3: LLM analysis
	if completed, confidence, reason := d.checkWithLLM(ctx, req); completed {
		return true, confidence, "llm:" + reason
	}

	return false, 0.0, ""
}

// checkStructuredOutput looks for task_status field in function calling results.
func (d *CompletionDetector) checkStructuredOutput(req *response.InterceptRequest) (bool, float64, string) {
	var resp map[string]interface{}
	if err := json.Unmarshal(req.ResponseBody, &resp); err != nil {
		return false, 0.0, ""
	}

	if choices, ok := resp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
					for _, tc := range toolCalls {
						if tcMap, ok := tc.(map[string]interface{}); ok {
							if fn, ok := tcMap["function"].(map[string]interface{}); ok {
								if args, ok := fn["arguments"].(string); ok {
									var taskStatus struct {
										Status string `json:"status"`
										Reason string `json:"reason"`
									}
									if err := json.Unmarshal([]byte(args), &taskStatus); err == nil {
										if taskStatus.Status == "completed" {
											return true, 1.0, taskStatus.Reason
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return false, 0.0, ""
}

// checkKeywords looks for completion keywords in the response.
func (d *CompletionDetector) checkKeywords(req *response.InterceptRequest) (bool, float64, string) {
	completionKeywords := []struct {
		keywords   []string
		confidence float64
	}{
		{keywords: []string{"任务完成", "全部完成", "执行完毕"}, confidence: 0.9},
		{keywords: []string{"task completed", "all done", "finished successfully"}, confidence: 0.9},
		{keywords: []string{"已完成", "完成了", "做完了"}, confidence: 0.85},
		{keywords: []string{"done", "completed", "finished"}, confidence: 0.75},
	}

	content := extractAssistantContent(req.ResponseBody)
	if content == "" {
		return false, 0.0, ""
	}

	contentLower := strings.ToLower(content)
	for _, group := range completionKeywords {
		for _, kw := range group.keywords {
			if strings.Contains(contentLower, strings.ToLower(kw)) {
				if d.hasCompletionContext(content) {
					return true, group.confidence, kw
				}
			}
		}
	}
	return false, 0.0, ""
}

// hasCompletionContext checks for additional context suggesting completion.
func (d *CompletionDetector) hasCompletionContext(content string) bool {
	contextKeywords := []string{"成功", "success", "successfully", "所有", "all", "everything"}
	contentLower := strings.ToLower(content)
	for _, kw := range contextKeywords {
		if strings.Contains(contentLower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// checkWithLLM uses LLM to analyze recent conversation for completion.
func (d *CompletionDetector) checkWithLLM(ctx context.Context, req *response.InterceptRequest) (bool, float64, string) {
	if d.llmCaller == nil {
		return false, 0.0, ""
	}

	model := "auto"
	prompt := `分析以下LLM响应，判断任务是否已完成。返回JSON: {"completed": true/false, "confidence": 0.0-1.0, "reason": "判断依据"}

LLM响应: ` + extractAssistantContent(req.ResponseBody)

	messages := []map[string]string{
		{"role": "system", "content": "你是任务完成度分析专家"},
		{"role": "user", "content": prompt},
	}

	resp, err := d.llmCaller.CallLLM(ctx, model, messages)
	if err != nil {
		slog.Warn("completion_detection_llm_failed", "error", err)
		return false, 0.0, ""
	}

	var result struct {
		Completed  bool    `json:"completed"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return false, 0.0, ""
	}

	if result.Completed && result.Confidence >= d.minConfidence() {
		return true, result.Confidence, result.Reason
	}
	return false, 0.0, ""
}

// extractAssistantContent extracts the assistant message content from response.
func extractAssistantContent(body []byte) string {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}

	if choices, ok := resp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].(string); ok {
					return content
				}
			}
		}
	}
	return ""
}
