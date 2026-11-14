package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ToolRecognizer implements Layer 3: LLM-based classification.
//
// Characteristics:
//  - Latency: 100-2000ms (depends on LLM provider)
//  - Cost: Moderate to high (API calls with tokens)
//  - Accuracy: 90-98% precision, handles ambiguous edge cases
//
// Strategy: Use a fast, cheap LLM (e.g., GPT-3.5, GLM-4-flash) to analyze
// the request when rule and context layers lack confidence.
// Design inspired by LangChain's LLM router chains.
type ToolRecognizer struct {
	llmProvider  LLMProvider
	promptTemplate string
	maxTokens    int
	timeout      time.Duration
}

// LLMProvider abstracts the LLM API for intent classification.
type LLMProvider interface {
	// Classify sends a classification request to the LLM
	Classify(ctx context.Context, prompt string, maxTokens int) (LLMResponse, error)

	// Name returns the provider identifier (e.g., "openai", "glm")
	Name() string

	// EstimatedCostUSD returns the approximate cost per request
	EstimatedCostUSD() float64
}

// LLMResponse contains the LLM's classification result.
type LLMResponse struct {
	Intent     IntentType
	Confidence float64
	Reasoning  string
	TokensUsed int
}

// NewToolRecognizer creates a Layer 3 LLM-based recognizer.
func NewToolRecognizer(provider LLMProvider) *ToolRecognizer {
	return &ToolRecognizer{
		llmProvider:    provider,
		promptTemplate: defaultClassificationPrompt(),
		maxTokens:      200, // Classification should be concise
		timeout:        2 * time.Second,
	}
}

// Recognize implements IntentRecognizer for Layer 3.
func (t *ToolRecognizer) Recognize(ctx context.Context, signals RecognitionSignals) (*IntentResult, error) {
	start := time.Now()

	// Apply timeout to LLM call
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	// Build classification prompt
	prompt := t.buildPrompt(signals)

	// Call LLM provider
	llmResp, err := t.llmProvider.Classify(ctx, prompt, t.maxTokens)
	if err != nil {
		return nil, fmt.Errorf("llm provider failed: %w", err)
	}

	// Build result
	result := &IntentResult{
		Primary:    llmResp.Intent,
		Confidence: llmResp.Confidence,
		Layer:      "tool",
		Reason:     fmt.Sprintf("LLM classification: %s", llmResp.Reasoning),
		Latency:    time.Since(start),
		Metadata: map[string]interface{}{
			"provider":     t.llmProvider.Name(),
			"tokens_used":  llmResp.TokensUsed,
			"cost_usd":     t.llmProvider.EstimatedCostUSD(),
			"llm_response": llmResp,
		},
	}

	return result, nil
}

func (t *ToolRecognizer) buildPrompt(signals RecognitionSignals) string {
	// Construct a structured prompt for the LLM
	// Truncate long prompts to stay within token budget
	systemPrompt := t.truncate(signals.SystemPrompt, 500)
	userPrompt := t.truncate(signals.LastUserPrompt, 1000)

	prompt := fmt.Sprintf(t.promptTemplate,
		systemPrompt,
		userPrompt,
		signals.MessageCount,
		signals.ToolCount,
		signals.HasImages,
		signals.HasCodeBlock,
		signals.ClientType,
	)

	return prompt
}

func (t *ToolRecognizer) truncate(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + "..."
}

func (t *ToolRecognizer) Name() string { return "tool" }
func (t *ToolRecognizer) Layer() int   { return 3 }

// defaultClassificationPrompt returns the template for LLM classification.
func defaultClassificationPrompt() string {
	return `You are an intent classifier for an LLM routing gateway. Analyze the request and classify it into ONE of these categories:

Categories:
- chat: General conversation
- reasoning: Mathematical/logical analysis requiring step-by-step thinking
- code: Programming, debugging, refactoring
- agent: Multi-tool agentic workflows
- creative: Writing, translation, summarization
- long_context: Large document analysis (>50k tokens)
- vision: Image understanding
- function_call: Simple 1-2 tool invocation
- code_audit: Code review, security analysis
- intent_classification: Text classification tasks
- planning: Technical design, proposals, roadmaps

Request details:
System prompt: %s
User prompt: %s
Message count: %d
Tool count: %d
Has images: %v
Has code blocks: %v
Client type: %s

Respond in JSON format:
{
  "intent": "<category>",
  "confidence": <0.0-1.0>,
  "reasoning": "<brief explanation>"
}

Be concise. Focus on the PRIMARY task being requested.`
}

// MockLLMProvider implements a mock LLM for testing.
type MockLLMProvider struct {
	responses map[string]LLMResponse
	callCount int
}

func NewMockLLMProvider() *MockLLMProvider {
	return &MockLLMProvider{
		responses: make(map[string]LLMResponse),
		callCount: 0,
	}
}

func (m *MockLLMProvider) Classify(ctx context.Context, prompt string, maxTokens int) (LLMResponse, error) {
	m.callCount++

	// Simulate latency
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		return LLMResponse{}, ctx.Err()
	}

	// Return mock response (in real implementation, parse LLM output)
	return LLMResponse{
		Intent:     IntentChat,
		Confidence: 0.85,
		Reasoning:  "mock classification",
		TokensUsed: 150,
	}, nil
}

func (m *MockLLMProvider) Name() string           { return "mock" }
func (m *MockLLMProvider) EstimatedCostUSD() float64 { return 0.0001 }

// GLMProvider implements LLM classification using GLM-4-flash.
type GLMProvider struct {
	apiKey   string
	endpoint string
	model    string
}

func NewGLMProvider(apiKey, endpoint string) *GLMProvider {
	return &GLMProvider{
		apiKey:   apiKey,
		endpoint: endpoint,
		model:    "glm-4-flash", // Fast and economical
	}
}

func (g *GLMProvider) Classify(ctx context.Context, prompt string, maxTokens int) (LLMResponse, error) {
	// Implementation would call GLM API here
	// For now, return a placeholder

	// In production:
	// 1. Construct API request with prompt
	// 2. Call GLM API with timeout from ctx
	// 3. Parse JSON response
	// 4. Extract intent, confidence, reasoning
	// 5. Return LLMResponse

	return LLMResponse{
		Intent:     IntentChat,
		Confidence: 0.80,
		Reasoning:  "GLM classification placeholder",
		TokensUsed: 150,
	}, fmt.Errorf("GLM provider not yet implemented")
}

func (g *GLMProvider) Name() string           { return "glm-4-flash" }
func (g *GLMProvider) EstimatedCostUSD() float64 { return 0.0002 } // Approximate

// OpenAIProvider implements LLM classification using GPT-3.5-turbo.
type OpenAIProvider struct {
	apiKey string
	model  string
}

func NewOpenAIProvider(apiKey string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey: apiKey,
		model:  "gpt-3.5-turbo", // Fast and economical
	}
}

func (o *OpenAIProvider) Classify(ctx context.Context, prompt string, maxTokens int) (LLMResponse, error) {
	// Implementation would call OpenAI API here
	// Similar structure to GLMProvider

	return LLMResponse{
		Intent:     IntentChat,
		Confidence: 0.82,
		Reasoning:  "OpenAI classification placeholder",
		TokensUsed: 160,
	}, fmt.Errorf("OpenAI provider not yet implemented")
}

func (o *OpenAIProvider) Name() string           { return "gpt-3.5-turbo" }
func (o *OpenAIProvider) EstimatedCostUSD() float64 { return 0.0003 }

// ParseLLMJSON parses the LLM's JSON response into a structured result.
func ParseLLMJSON(jsonStr string) (LLMResponse, error) {
	var resp struct {
		Intent     string  `json:"intent"`
		Confidence float64 `json:"confidence"`
		Reasoning  string  `json:"reasoning"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return LLMResponse{}, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Validate intent type
	intent := IntentType(resp.Intent)
	validIntents := map[IntentType]bool{
		IntentChat: true, IntentReasoning: true, IntentCode: true,
		IntentAgent: true, IntentCreative: true, IntentLongContext: true,
		IntentVision: true, IntentFunctionCall: true, IntentCodeAudit: true,
		IntentIntentClassification: true, IntentPlanning: true,
	}

	if !validIntents[intent] {
		return LLMResponse{}, fmt.Errorf("invalid intent type: %s", resp.Intent)
	}

	// Clamp confidence to [0, 1]
	if resp.Confidence < 0 {
		resp.Confidence = 0
	} else if resp.Confidence > 1 {
		resp.Confidence = 1
	}

	return LLMResponse{
		Intent:     intent,
		Confidence: resp.Confidence,
		Reasoning:  resp.Reasoning,
	}, nil
}
