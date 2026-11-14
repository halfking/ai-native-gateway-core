// Package security implements a modular, multi-layer security detection system
// for both output compliance checking and input injection detection.
//
// Architecture: Three-layer detection with precision-speed-cost trade-off:
//
//  Layer 1 (Rule-based):    Fast (μs), zero cost, high precision on clear patterns
//  Layer 2 (Context-based): Balanced (ms), low cost, reduces false positives
//  Layer 3 (LLM-based):     Slow (s), moderate cost, handles ambiguous cases
//
// Design inspired by:
//  - Intent recognition system: Multi-layer progressive escalation
//  - Rasa NLU: Confidence-based threshold escalation
//  - Defense in depth: Multiple independent detection layers
//
// Key improvements over pure regex detection:
//  - Context-aware validation reduces false positives
//  - Confidence scoring enables intelligent escalation
//  - LLM layer handles semantic understanding
//  - Performance metrics for optimization
//
// Public API entry points:
//  - NewSecurityPipeline(): Builds a complete 3-layer detection pipeline
//  - SecurityDetector interface: Pluggable detection layers
//  - SecurityResult: Unified output with confidence and context
package security

import (
	"context"
	"time"
)

// ThreatType represents the type of security threat detected.
type ThreatType string

const (
	// Output compliance threats
	ThreatPII                ThreatType = "pii"
	ThreatCreditCard         ThreatType = "credit_card"
	ThreatEmail              ThreatType = "email"
	ThreatPhone              ThreatType = "phone"
	ThreatSSN                ThreatType = "ssn"
	ThreatIDCard             ThreatType = "id_card"
	ThreatPassword           ThreatType = "password"
	ThreatAPIKey             ThreatType = "api_key"
	ThreatInternalIP         ThreatType = "internal_ip"
	ThreatToxicity           ThreatType = "toxicity"
	ThreatBias               ThreatType = "bias"
	ThreatHallucination      ThreatType = "hallucination"
	
	// Input injection threats
	ThreatPromptInjection    ThreatType = "prompt_injection"
	ThreatJailbreak          ThreatType = "jailbreak"
	ThreatSystemPromptLeak   ThreatType = "system_prompt_leak"
	ThreatRoleManipulation   ThreatType = "role_manipulation"
	ThreatInstructionOverride ThreatType = "instruction_override"
)

// DetectionContext provides contextual information for threat detection.
type DetectionContext struct {
	// Content being analyzed
	Text     string
	Language string // "zh", "en", "mixed"
	
	// Context from the conversation
	IsSystemPrompt   bool
	IsUserMessage    bool
	IsAssistantMessage bool
	IsToolOutput     bool
	
	// Session context
	SessionID       string
	TenantID        string
	UserRole        string
	PreviousThreats []ThreatType
	
	// Technical context
	ModelName       string
	IsStreamingMode bool
	MessageIndex    int
	TotalMessages   int
}

// ThreatMatch represents a detected security threat.
type ThreatMatch struct {
	// Threat identification
	Type     ThreatType
	Severity int // 1-10, where 10 is critical
	
	// Location in text
	Start int
	End   int
	Value string // The actual detected content
	
	// Detection metadata
	RuleID      string
	Pattern     string
	Confidence  float64 // 0.0-1.0
	
	// Context
	Context     string // Surrounding text for context
	Reason      string // Why this was flagged
	
	// Recommended action
	Action      string // "block", "redact", "warn", "log"
	Replacement string // Suggested replacement for redaction
}

// SecurityResult is the output of security detection.
type SecurityResult struct {
	// Overall assessment
	IsSafe     bool
	Confidence float64 // 0.0-1.0
	
	// Detected threats
	Threats []ThreatMatch
	
	// Layer information
	Layer   string // "rule", "context", "llm", "chained"
	Latency time.Duration
	
	// Recommended actions
	ShouldBlock  bool
	ShouldRedact bool
	RedactedText string
	
	// Metadata
	Reason   string
	Metadata map[string]interface{}
}

// SecurityDetector is the core interface for security detection layers.
//
// Each layer operates independently and returns results with confidence.
// Higher layers can use lower-layer results as input for refinement.
//
// Thread-safety: Implementations must be safe for concurrent calls.
type SecurityDetector interface {
	// Detect analyzes content and returns detected threats.
	// Returns error only on infrastructure failures (e.g., LLM API down).
	// Low confidence should be expressed via SecurityResult.Confidence, not error.
	Detect(ctx context.Context, content string, detectionCtx DetectionContext) (*SecurityResult, error)
	
	// Name returns the detector's identifier (e.g., "rule", "context", "llm")
	Name() string
	
	// Layer returns the architectural layer (1=rule, 2=context, 3=llm)
	Layer() int
}

// SecurityPipelineConfig controls the behavior of the security pipeline.
type SecurityPipelineConfig struct {
	// ConfidenceThreshold is the minimum confidence to accept a result
	// without escalating to the next layer. Default: 0.75
	ConfidenceThreshold float64
	
	// MaxLatency is the total time budget across all layers.
	// If exceeded, return the best available result. Default: 200ms
	MaxLatency time.Duration
	
	// EnableCaching enables result caching by content hash
	EnableCaching bool
	
	// CacheTTL controls how long cached results remain valid
	CacheTTL time.Duration
	
	// StrictMode requires LLM validation even for high-confidence rule matches
	StrictMode bool
	
	// EnabledThreatTypes limits which threats to detect
	// Empty slice = detect all types
	EnabledThreatTypes []ThreatType
	
	// BlockThreshold is the severity level above which content is blocked
	// Default: 8 (critical threats only)
	BlockThreshold int
	
	// RedactThreshold is the severity level above which content is redacted
	// Default: 6 (moderate and above)
	RedactThreshold int
}

// DefaultSecurityPipelineConfig returns production-ready defaults.
func DefaultSecurityPipelineConfig() SecurityPipelineConfig {
	return SecurityPipelineConfig{
		ConfidenceThreshold: 0.75,
		MaxLatency:          200 * time.Millisecond,
		EnableCaching:       true,
		CacheTTL:            10 * time.Minute,
		StrictMode:          false,
		EnabledThreatTypes:  nil, // All types
		BlockThreshold:      8,
		RedactThreshold:     6,
	}
}

// ValidationResult provides additional context about a potential threat.
type ValidationResult struct {
	IsValid      bool    // Is this a real threat?
	Confidence   float64 // 0.0-1.0
	Reason       string
	SuggestedAction string // "ignore", "warn", "redact", "block"
}

// ContextValidator validates threats using contextual information.
type ContextValidator interface {
	// Validate examines a potential threat in context to determine if it's real.
	// This reduces false positives from pure pattern matching.
	Validate(ctx context.Context, match ThreatMatch, detectionCtx DetectionContext) (*ValidationResult, error)
}

// SecurityCache provides caching for detection results.
type SecurityCache interface {
	// Get retrieves a cached result by content hash
	Get(contentHash string) (*SecurityResult, bool)
	
	// Set stores a result with TTL
	Set(contentHash string, result *SecurityResult)
	
	// Invalidate clears the cache for a specific hash
	Invalidate(contentHash string)
}

// ThreatScore pairs a threat type with its computed likelihood.
type ThreatScore struct {
	Type  ThreatType `json:"type"`
	Score float64    `json:"score"` // 0.0-1.0
}
