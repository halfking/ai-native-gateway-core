// Package compressor - task_analyzer.go (v4 T2)
//
// Task completion detection for the v4 intelligent session compressor.
//
// Problem:
//
//	LLM conversations often contain multiple tasks or sub-tasks.
//	Once a task is complete (user says "done", "next", "thanks"),
//	the details of that task are no longer needed in the compressed
//	context. The LLM summary should focus on:
//	  1. Active/in-progress tasks
//	  2. Completed task outcomes (not the step-by-step process)
//	  3. Pending items
//
// Detection strategies:
//
//	1. Explicit completion markers: "done", "completed", "next task"
//	2. Topic boundaries: user introduces a new topic/task
//	3. Tool round boundaries: a completed tool round without follow-up
//	4. Turn-pair analysis: user asks → assistant answers → user accepts
//
// This module provides the analysis functions. The actual filtering
// is done by the compressor orchestrator (session_compressor.go).

package compressor

// TaskBoundary describes a detected task boundary in the conversation.
type TaskBoundary struct {
	// StartIdx is the message index where this task begins.
	StartIdx int `json:"start_idx"`
	// EndIdx is the message index where this task ends (inclusive).
	EndIdx int `json:"end_idx"`
	// Completed is true when the task appears to be complete.
	Completed bool `json:"completed"`
	// Description is a brief description of the task.
	Description string `json:"description"`
}

// TaskAnalysisResult is the output of AnalyzeTasks.
type TaskAnalysisResult struct {
	// Boundaries are the detected task boundaries.
	Boundaries []TaskBoundary `json:"boundaries"`
	// CompletedCount is the number of completed tasks.
	CompletedCount int `json:"completed_count"`
	// ActiveCount is the number of in-progress tasks.
	ActiveCount int `json:"active_count"`
	// HasAnalysis is true when task analysis was performed.
	HasAnalysis bool `json:"has_analysis"`
}

// CompletionSignal represents a single completion signal in a message.
type CompletionSignal struct {
	MessageIdx   int    `json:"message_idx"`
	Signal       string `json:"signal"`
	IsCompletion bool   `json:"is_completion"`
}

// completionPatterns are patterns that indicate task completion.
// These are used for heuristic detection (not exhaustive).
var completionPatterns = []string{
	"done", "completed", "finished",
	"next task", "next question", "next step",
	"that's all", "that is all", "all done",
	"thank you", "thanks",
	"proceed", "continue with next",
	"move on", "go ahead",
}

// newTaskPatterns are patterns that indicate a new task starting.
var newTaskPatterns = []string{
	"new task", "next task", "another task",
	"now I need", "now I want",
	"first,", "firstly",
	"next,", "next:",
	"another thing",
	"also,", "additionally",
	"what about",
}

// AnalyzeTasks analyzes message sequence for task boundaries.
// Uses heuristic patterns to detect:
//  1. User signals completion
//  2. New task introductions
//  3. Tool round boundaries
//
// Returns task boundaries. Empty slice = no clear boundaries found.
//
// Note: This is a heuristic analyzer. For production use, consider
// integrating with the LLM summary (compaction.go) which produces
// structured task segment output in the "Completed Work" and
// "Active Work" sections.
func AnalyzeTasks(msgs []map[string]any) *TaskAnalysisResult {
	result := &TaskAnalysisResult{}

	if len(msgs) < 4 {
		return result
	}

	// Detect completion signals from user messages
	var completionSignals []CompletionSignal
	for i, msg := range msgs {
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}

		content := extractTextContent(msg)
		if content == "" {
			continue
		}

		signal := detectCompletionSignal(content)
		if signal != "" {
			completionSignals = append(completionSignals, CompletionSignal{
				MessageIdx:   i,
				Signal:       signal,
				IsCompletion: true,
			})
		}

		if detectNewTaskStart(content) {
			completionSignals = append(completionSignals, CompletionSignal{
				MessageIdx:   i,
				Signal:       "new_task",
				IsCompletion: false,
			})
		}
	}

	if len(completionSignals) == 0 {
		return result
	}

	// Build task boundaries from completion signals
	// Audit P1 fix (2026-06-22): boundary 应由 completion signal 触发,
	// 不是 new_task signal. 修复前 bug: 同消息含 "done" + "next task" 时,
	// new_task 信号覆盖 completion 信号, 导致 CompletedCount=0.
	// 修复后: completion 信号触发 boundary, new_task 只设 lastEnd.
	// 同时防御: completion signal MessageIdx <= lastEnd 时跳过 (避免
	// end < start 的负向 boundary).
	var boundaries []TaskBoundary
	lastEnd := -1

	for _, signal := range completionSignals {
		if signal.IsCompletion {
			// Skip if this completion is at or before the current boundary end
			// (can happen when same message produces both completion and new_task signals)
			if signal.MessageIdx <= lastEnd+1 {
				continue
			}
			// Completion signal: previous task ends here
			boundaries = append(boundaries, TaskBoundary{
				StartIdx:   lastEnd + 1,
				EndIdx:     signal.MessageIdx - 1,
				Completed:  true,
				Description: signal.Signal,
			})
			result.CompletedCount++
			lastEnd = signal.MessageIdx
			continue
		}
		// new_task signal: just update lastEnd if not set
		if lastEnd < 0 {
			lastEnd = signal.MessageIdx
		}
	}

	// Add active task (from last completion to end)
	if lastEnd < len(msgs)-1 {
		boundaries = append(boundaries, TaskBoundary{
			StartIdx:   lastEnd + 1,
			EndIdx:     len(msgs) - 1,
			Completed:  false,
			Description: "active_task",
		})
		result.ActiveCount++
	}

	result.Boundaries = boundaries
	result.HasAnalysis = true
	return result
}

// detectCompletionSignal checks if a user message signals task completion.
// Returns the matched signal pattern, or empty string.
func detectCompletionSignal(content string) string {
	lower := stringsToLower(content)
	for _, pattern := range completionPatterns {
		if stringsContains(lower, pattern) {
			return pattern
		}
	}
	return ""
}

// detectNewTaskStart checks if a user message introduces a new task.
func detectNewTaskStart(content string) bool {
	lower := stringsToLower(content)
	for _, pattern := range newTaskPatterns {
		if stringsContains(lower, pattern) {
			return true
		}
	}
	return false
}

// extractTextContent extracts text content from a message map.
func extractTextContent(msg map[string]any) string {
	content, ok := msg["content"]
	if !ok {
		return ""
	}

	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, part := range c {
			if p, ok := part.(map[string]any); ok {
				if p["type"] == "text" {
					if t, ok := p["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
		}
		result := ""
		for _, t := range texts {
			result += t + "\n"
		}
		return result
	}
	return ""
}

// string functions (avoid importing strings for build speed)
func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func stringsToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}