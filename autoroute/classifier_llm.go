package autoroute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// LLMFallbackClassifier is invoked when the HeuristicClassifier returns
// a confidence below LLMConfidenceThreshold. It asks an LLM (using one of
// our cheap/free chat credentials) to choose among the current task types.
// The prompt contract is generated from AllTaskTypes and currently includes
// chat, reasoning, code, agent, creative, long_context, vision,
// function_call, code_audit, intent_classification, and planning.
//
// In v2.0 the LLM is a side-channel call: it does NOT generate the actual
// response — only the task type. The actual response is then routed to
// the chosen model via the normal flow.
//
// Failure handling:
//   - LLM call fails/times out (classifier-level cap follows
//     LLMGatewayAutoLLMTimeout, default 3s, clamped ≤30s) → return error,
//     decider falls back to heuristic result even at low confidence
//   - LLM call returns invalid output → return error, same fallback
//   - LLM call succeeds → return Classification with Classifier="llm"
type LLMFallbackClassifier struct {
	// Caller invokes the LLM with a tiny classification prompt and
	// receives back the raw task type string. Returns "" on failure.
	Caller func(ctx context.Context, prompt string) (string, error)

	// Timeout caps the LLM call. Default: 3s.
	timeout time.Duration
}

// NewLLMFallbackClassifier wraps a caller function. The caller is
// typically a thin shim around the chat completions endpoint that uses
// the cheapest available credential (system-internal API key).
func NewLLMFallbackClassifier(caller func(ctx context.Context, prompt string) (string, error)) *LLMFallbackClassifier {
	c := &LLMFallbackClassifier{Caller: caller, timeout: 3 * time.Second}
	// 2026-09-15 O4 verification fix: the self-loop classification path
	// (gateway → its own /v1/chat/completions → upstream) needs more than
	// the 3s default, but LLMGatewayAutoLLMTimeout only raised the HTTP
	// client's timeout while this classifier-level cap still cut every
	// call at 3s. Honor the same env knob here (seconds, clamped to ≤30
	// so a misconfig cannot stall the request path for long — this runs
	// before dispatch on low-confidence requests).
	if v := strings.TrimSpace(os.Getenv("LLMGatewayAutoLLMTimeout")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			if secs > 30 {
				secs = 30
			}
			c.timeout = time.Duration(secs) * time.Second
		}
	}
	return c
}

// Name implements Classifier.
func (c *LLMFallbackClassifier) Name() string { return "llm" }

// Classify implements Classifier. Builds a tiny classification prompt,
// calls the LLM, parses the response.
//
// Prompt template:
//
//	You are a request classifier. Choose ONE task type from the current
//	allowlist. The prompt provides bounded system/user text plus structural
//	signals (tools, tool results, images, code, token estimate) so the LLM
//	can apply the same task context as the heuristic classifier.
//
//	Return ONLY the task type string, nothing else.
//
// Both text inputs are capped before interpolation to keep classification
// inexpensive and prevent a large system prompt from consuming the request
// budget. The LLM never receives tool-result contents, only their presence.
func (c *LLMFallbackClassifier) Classify(ctx context.Context, sigs ClassificationSignals) (*Classification, error) {
	if c.Caller == nil {
		return nil, fmt.Errorf("llm classifier: caller not configured")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	prompt := buildClassificationPrompt(sigs)
	raw, err := c.Caller(timeoutCtx, prompt)
	if err != nil {
		if errors.Is(err, ErrLLMDisabled) {
			// Disabled deployments (no LLMGatewayAutoLLMEndpoint) return
			// DisabledCaller directly, bypassing InstrumentedCaller — without
			// this record the "disabled" outcome promised by the
			// llm_gateway_llm_classifier_total HELP is never emitted.
			RecordLLMMetricCall("disabled", 0)
		}
		return nil, fmt.Errorf("llm classify: %w", err)
	}
	task, ok := normaliseLLMTaskType(raw)
	if !ok {
		return nil, fmt.Errorf("llm classify: invalid task type %q", raw)
	}
	confidence := 0.85 // LLM responses are trusted more than heuristic
	return &Classification{
		Primary:    task,
		Confidence: confidence,
		Secondary:  []TaskScore{{Task: task, Score: confidence}},
		Signals:    sigs,
		Classifier: "llm",
		Reason:     "llm fallback (heuristic confidence < threshold)",
	}, nil
}

// buildClassificationPrompt is the bounded LLM fallback contract. It mirrors
// the heuristic classifier's task vocabulary and structural signals, while
// capping user/system text so a fallback cannot consume the request budget.
const (
	llmFallbackUserPromptLimit   = 1024
	llmFallbackSystemPromptLimit = 512
)

func buildClassificationPrompt(sigs ClassificationSignals) string {
	prompt := "You are a request classifier. Choose ONE task type from this allowlist:\n" +
		"  - chat                  : ordinary conversation or Q&A\n" +
		"  - reasoning             : math, logic, multi-step analysis\n" +
		"  - code                  : code generation, debugging, refactoring\n" +
		"  - agent                 : multi-step workflow with several tools\n" +
		"  - creative              : writing, translation, summarisation\n" +
		"  - long_context          : very long document (>50k tokens)\n" +
		"  - vision                : request contains image input\n" +
		"  - function_call         : 1-2 tool/function calls\n" +
		"  - code_audit            : code review, security, quality checks\n" +
		"  - intent_classification : intent, text, or sentiment classification\n" +
		"  - planning              : project plan, design, task breakdown, roadmap\n\n" +
		"Use the bounded system/user text and structural signals below. Tool-result contents are omitted.\n\n"

	prompt += fmt.Sprintf("Signals: tool_count=%d, has_tool_results=%t, has_images=%t, has_code_block=%t, estimated_tokens=%d\n\n",
		sigs.ToolCount, sigs.HasToolResults, sigs.HasImages, sigs.HasCodeBlock, sigs.EstimatedTokens)
	if sigs.SystemPrompt != "" {
		prompt += "System prompt:\n\"\"\"\n" + truncateLLMFallbackPromptText(sigs.SystemPrompt, llmFallbackSystemPromptLimit) + "\n\"\"\"\n\n"
	}
	prompt += "User prompt:\n\"\"\"\n" + truncateLLMFallbackPromptText(sigs.LastUserPrompt, llmFallbackUserPromptLimit) + "\n\"\"\"\n\n"
	prompt += "Return ONLY the task type string, nothing else.\n"
	return prompt
}

func truncateLLMFallbackPromptText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n...[truncated]"
}

// normaliseLLMTaskType maps the LLM's free-text answer to a TaskType.
// Tolerant to surrounding whitespace and minor variations like
// "code generation" or "agentic task".
func normaliseLLMTaskType(raw string) (TaskType, bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	// Strip trailing punctuation
	for len(s) > 0 && (s[len(s)-1] == '.' || s[len(s)-1] == ',' || s[len(s)-1] == ';' || s[len(s)-1] == '!') {
		s = s[:len(s)-1]
	}
	// Direct match
	for _, t := range AllTaskTypes {
		if string(t) == s {
			return t, true
		}
	}
	// Fuzzy: contains as substring
	for _, t := range AllTaskTypes {
		if strings.Contains(s, string(t)) {
			return t, true
		}
	}
	// Aliases
	switch s {
	case "math", "logic", "analytical":
		return TaskReasoning, true
	case "programming", "coding", "software":
		return TaskCode, true
	case "writing", "translation", "summarization", "summarisation":
		return TaskCreative, true
	case "tool_use", "tools", "function", "function_calling":
		return TaskFunctionCall, true
	case "image", "multimodal", "visual":
		return TaskVision, true
	case "long", "document":
		return TaskLongContext, true
	case "agentic", "autonomous":
		return TaskAgent, true
	case "conversation", "general", "qa":
		return TaskChat, true
	}
	slog.Warn("llm classify: unknown task type", "raw", raw, "normalised", s)
	return "", false
}
