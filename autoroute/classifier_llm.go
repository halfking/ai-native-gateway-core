package autoroute

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LLMFallbackClassifier is invoked when the HeuristicClassifier returns
// a confidence below LLMConfidenceThreshold. It asks an LLM (using one of
// our cheap/free chat credentials) to choose among the 8 task types.
//
// In v2.0 the LLM is a side-channel call: it does NOT generate the actual
// response — only the task type. The actual response is then routed to
// the chosen model via the normal flow.
//
// Failure handling:
//   - LLM call times out (3s) → return error, decider falls back to
//     heuristic result even at low confidence
//   - LLM call returns invalid output → return error, same fallback
//   - LLM call succeeds → return Classification with Classifier="llm"
type LLMFallbackClassifier struct {
	// Caller invokes the LLM with a tiny classification prompt and
	// receives back the raw task type string. Returns "" on failure.
	Caller func(ctx context.Context, prompt string) (string, error)

	// Timeout caps the LLM call. Default: 3s.
	Timeout context.Context // unused — caller is responsible
	timeout time.Duration
}

// NewLLMFallbackClassifier wraps a caller function. The caller is
// typically a thin shim around the chat completions endpoint that uses
// the cheapest available credential (system-internal API key).
func NewLLMFallbackClassifier(caller func(ctx context.Context, prompt string) (string, error)) *LLMFallbackClassifier {
	return &LLMFallbackClassifier{Caller: caller, timeout: 3 * time.Second}
}

// Name implements Classifier.
func (c *LLMFallbackClassifier) Name() string { return "llm" }

// Classify implements Classifier. Builds a tiny classification prompt,
// calls the LLM, parses the response.
//
// Prompt template:
//
//   You are a request classifier. Choose ONE task type from this list
//   that best matches the user's request:
//     - chat          : ordinary conversation
//     - reasoning     : math, logic, multi-step analysis
//     - code          : code generation, debugging, refactoring
//     - agent         : multi-step agentic workflow with many tools
//     - creative      : writing, translation, summarisation
//     - long_context  : very long document (>50k tokens)
//     - vision        : request contains image input
//     - function_call : 1-2 tool/function calls
//
//   Return ONLY the task type string, nothing else.
//
//   User prompt:
//   """
//   <last user prompt>
//   """
//
// We deliberately don't pass the full system prompt to keep the
// classifier call cheap (<500 tokens in, ~5 tokens out).
func (c *LLMFallbackClassifier) Classify(ctx context.Context, sigs ClassificationSignals) (*Classification, error) {
	if c.Caller == nil {
		return nil, fmt.Errorf("llm classifier: caller not configured")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	prompt := buildClassificationPrompt(sigs)
	raw, err := c.Caller(timeoutCtx, prompt)
	if err != nil {
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

// buildClassificationPrompt is the template shown above, formatted
// with the actual signals.
func buildClassificationPrompt(sigs ClassificationSignals) string {
	prompt := "You are a request classifier. Choose ONE task type from this list that best matches the user's request:\n" +
		"  - chat          : ordinary conversation\n" +
		"  - reasoning     : math, logic, multi-step analysis\n" +
		"  - code          : code generation, debugging, refactoring\n" +
		"  - agent         : multi-step agentic workflow with many tools\n" +
		"  - creative      : writing, translation, summarisation\n" +
		"  - long_context  : very long document (>50k tokens)\n" +
		"  - vision        : request contains image input\n" +
		"  - function_call : 1-2 tool/function calls\n\n" +
		"Return ONLY the task type string, nothing else.\n\n"

	if sigs.ToolCount > 0 {
		prompt += fmt.Sprintf("Tool count: %d\n", sigs.ToolCount)
	}
	if sigs.EstimatedTokens > 0 {
		prompt += fmt.Sprintf("Estimated tokens: %d\n", sigs.EstimatedTokens)
	}
	if sigs.HasImages {
		prompt += "Has images: true\n"
	}
	if sigs.HasCodeBlock {
		prompt += "Has code block: true\n"
	}
	prompt += "\nUser prompt:\n\"\"\"\n" + sigs.LastUserPrompt + "\n\"\"\"\n"
	return prompt
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