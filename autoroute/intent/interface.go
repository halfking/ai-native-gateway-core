// Package intent implements a modular, pluggable intent recognition system
// for auto-routing optimization.
//
// Architecture: Three-layer classification with accuracy-speed-cost trade-off:
//
//  Layer 1 (Rule-based):    Fast (μs), economical, high precision on clear signals
//  Layer 2 (Context-based): Balanced (ms), moderate cost, pattern matching
//  Layer 3 (Tool-based):    Slow (s), expensive, highest accuracy via LLM
//
// Design inspired by open-source leaders:
//  - Rasa: Multi-stage NLU pipeline with confidence thresholds
//  - Haystack: Plugin architecture with fallback chains
//  - LangChain: Router chains with escalation policies
//
// Public API entry points:
//  - NewChainedRecognizer(): Builds a complete 3-layer pipeline
//  - IntentRecognizer interface: Pluggable classifiers
//  - IntentResult: Unified output with confidence scoring
package intent

import (
	"context"
	"time"
)

// IntentType represents the classified user intent/task type.
// Values align with autoroute.TaskType for backward compatibility.
type IntentType string

const (
	// IntentChat is the default conversational intent
	IntentChat IntentType = "chat"
	// IntentReasoning covers mathematical/logical analysis
	IntentReasoning IntentType = "reasoning"
	// IntentCode covers programming tasks
	IntentCode IntentType = "code"
	// IntentAgent covers multi-tool agentic workflows
	IntentAgent IntentType = "agent"
	// IntentCreative covers writing/translation/summarization
	IntentCreative IntentType = "creative"
	// IntentLongContext covers large-context requests
	IntentLongContext IntentType = "long_context"
	// IntentVision covers image understanding
	IntentVision IntentType = "vision"
	// IntentFunctionCall covers simple tool invocation
	IntentFunctionCall IntentType = "function_call"
	// IntentCodeAudit covers code review/security analysis
	IntentCodeAudit IntentType = "code_audit"
	// IntentIntentClassification covers text classification tasks
	IntentIntentClassification IntentType = "intent_classification"
	// IntentPlanning covers planning/design documents
	IntentPlanning IntentType = "planning"
)

// RecognitionSignals contains extracted features from the request.
// This is the normalized input fed to all recognizer layers.
type RecognitionSignals struct {
	// Textual features
	SystemPrompt   string
	LastUserPrompt string
	Language       string // "zh", "en", "mixed"

	// Structural features
	MessageCount    int
	EstimatedTokens int
	ToolCount       int
	HasToolResults  bool
	HasImages       bool
	HasCodeBlock    bool

	// Contextual features
	ClientType string // IDE fingerprint (e.g., "cursor", "vscode")
	AgentRole  string // Session role identifier

	// Session context (for context-based layer)
	SessionID      string
	PreviousIntent IntentType
	RequestCount   int
}

// IntentResult is the output of intent recognition.
type IntentResult struct {
	// Primary is the highest-confidence intent
	Primary IntentType

	// Confidence is 0.0-1.0, indicating certainty
	Confidence float64

	// Alternatives lists other possible intents with scores
	Alternatives []IntentScore

	// Layer identifies which recognizer produced this result
	// One of: "rule", "context", "tool", "chained"
	Layer string

	// Reason explains the classification decision (for observability)
	Reason string

	// Latency records the time taken by this layer
	Latency time.Duration

	// Metadata contains layer-specific diagnostic information
	Metadata map[string]interface{}
}

// IntentScore pairs an intent type with its computed score.
type IntentScore struct {
	Intent IntentType `json:"intent"`
	Score  float64    `json:"score"`
}

// IntentRecognizer is the core interface implemented by all classification layers.
//
// Each layer operates independently and returns a result with confidence.
// Higher layers (context, tool) can use lower-layer results as input.
//
// Thread-safety: Implementations must be safe for concurrent calls.
type IntentRecognizer interface {
	// Recognize analyzes signals and returns the classified intent.
	// Returns error only on infrastructure failures (e.g., LLM API down).
	// Low confidence should be expressed via IntentResult.Confidence, not error.
	Recognize(ctx context.Context, signals RecognitionSignals) (*IntentResult, error)

	// Name returns the recognizer's identifier (e.g., "rule", "context", "llm")
	Name() string

	// Layer returns the architectural layer (1=rule, 2=context, 3=tool)
	Layer() int
}

// ChainConfig controls the behavior of a chained recognizer pipeline.
type ChainConfig struct {
	// ConfidenceThreshold is the minimum confidence to accept a result
	// without escalating to the next layer. Default: 0.7
	ConfidenceThreshold float64

	// MaxLatency is the total time budget across all layers.
	// If exceeded, return the best available result. Default: 500ms
	MaxLatency time.Duration

	// EnableCaching enables session-level intent caching
	EnableCaching bool

	// CacheTTL controls how long cached intents remain valid
	CacheTTL time.Duration

	// FallbackIntent is returned if all layers fail
	FallbackIntent IntentType
}

// DefaultChainConfig returns production-ready defaults.
func DefaultChainConfig() ChainConfig {
	return ChainConfig{
		ConfidenceThreshold: 0.7,
		MaxLatency:          500 * time.Millisecond,
		EnableCaching:       true,
		CacheTTL:            5 * time.Minute,
		FallbackIntent:      IntentChat,
	}
}

// FeatureExtractor is the interface for extracting signals from raw requests.
// This decouples signal extraction from classification logic.
type FeatureExtractor interface {
	// Extract derives RecognitionSignals from a raw request body
	Extract(ctx context.Context, rawRequest interface{}) (RecognitionSignals, error)
}

// IntentCache provides session-level caching for recognized intents.
type IntentCache interface {
	// Get retrieves a cached intent for the session
	Get(sessionID string) (*IntentResult, bool)

	// Set stores an intent result for the session
	Set(sessionID string, result *IntentResult)

	// Invalidate clears the cache for a session
	Invalidate(sessionID string)
}
