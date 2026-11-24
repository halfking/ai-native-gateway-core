// Package examples provides reference implementations of external hooks.
//
// compliance_hook demonstrates a simple content compliance checker that:
//   - Blocks requests containing banned keywords
//   - Logs response metadata for audit trails
//   - Shows W3.2/W3.3 semantics in practice
package examples

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// ComplianceHook is a simple external hook that enforces content policies.
//
// This demonstrates the minimal viable external hook:
//   - onRequest: check for banned content, block if found
//   - onResponse: log metadata for audit (no content modification)
//   - State flow: request metadata → response correlation
type ComplianceHook struct {
	// BannedKeywords are case-insensitive patterns that trigger blocks
	BannedKeywords []string
	// AuditLog collects request/response pairs for compliance reporting
	AuditLog []AuditEntry
}

// AuditEntry records a request-response pair for compliance review.
type AuditEntry struct {
	RequestID   string
	SessionID   string
	Model       string
	Blocked     bool
	BlockReason string
	InputTokens int
	OutputTokens int
}

// NewComplianceHook creates a hook with default banned keywords.
func NewComplianceHook() *ComplianceHook {
	return &ComplianceHook{
		BannedKeywords: []string{
			"exploit", "hack", "bypass", "jailbreak",
			"ignore previous instructions", "disregard safety",
		},
		AuditLog: make([]AuditEntry, 0),
	}
}

// OnRequest checks for banned content in the request body.
//
// W3.2 semantics demonstrated:
//   - Returns DecisionBlock if banned content found
//   - Sets ctx.State for onResponse correlation
//   - Does not modify request (no rewrite)
func (h *ComplianceHook) OnRequest(inv *contract.Invocation) *contract.Reply {
	if inv == nil || inv.Context == nil || inv.Request == nil {
		return &contract.Reply{
			ContractVersion: contract.ContractVersion,
			Decision:        contract.DecisionContinue,
			Error:           "invalid invocation",
		}
	}

	// Extract request body text
	bodyText := string(inv.Request.Body.Body)
	lowerBody := strings.ToLower(bodyText)

	// Check for banned keywords
	for _, keyword := range h.BannedKeywords {
		if strings.Contains(lowerBody, strings.ToLower(keyword)) {
			// Block the request
			return &contract.Reply{
				ContractVersion: contract.ContractVersion,
				Decision:        contract.DecisionBlock,
				Reason:          fmt.Sprintf("content policy violation: banned keyword '%s'", keyword),
			}
		}
	}

	// Extract metadata for audit correlation
	inputTokens := estimateTokens(bodyText)
	
	// Set state for onResponse correlation (W3.3)
	if inv.Context.State == nil {
		inv.Context.State = make(map[string]string)
	}
	inv.Context.State["input_tokens"] = fmt.Sprintf("%d", inputTokens)
	inv.Context.State["model"] = inv.Request.Model
	inv.Context.State["blocked"] = "false"

	return &contract.Reply{
		ContractVersion: contract.ContractVersion,
		Decision:        contract.DecisionContinue,
	}
}

// OnResponse logs metadata for compliance audit.
//
// W3.3 semantics demonstrated:
//   - Receives state from onRequest via ctx.State
//   - Does not modify response (audit only)
//   - Works even if client disconnected (fire-and-forget)
func (h *ComplianceHook) OnResponse(inv *contract.Invocation) *contract.Reply {
	if inv == nil || inv.Context == nil || inv.Response == nil {
		return &contract.Reply{
			ContractVersion: contract.ContractVersion,
			Decision:        contract.DecisionContinue,
			Error:           "invalid invocation",
		}
	}

	// Restore state from onRequest (W3.3 correlation)
	inputTokens := 0
	if val := inv.Context.State["input_tokens"]; val != "" {
		fmt.Sscanf(val, "%d", &inputTokens)
	}
	model := inv.Context.State["model"]
	blocked := inv.Context.State["blocked"] == "true"

	// Extract response metadata
	outputTokens := estimateTokens(string(inv.Response.Body.Body))

	// Log audit entry
	entry := AuditEntry{
		RequestID:    inv.Context.RequestID,
		SessionID:    inv.Context.Session,
		Model:        model,
		Blocked:      blocked,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	h.AuditLog = append(h.AuditLog, entry)

	// No response modification (audit only)
	return &contract.Reply{
		ContractVersion: contract.ContractVersion,
		Decision:        contract.DecisionContinue,
	}
}

// estimateTokens provides rough token count (1 token ≈ 4 chars)
func estimateTokens(text string) int {
	return len(text) / 4
}

// ExtractMessages attempts to parse OpenAI-style request body.
func ExtractMessages(body []byte) ([]map[string]interface{}, error) {
	var payload struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Messages, nil
}

// ContainsBannedContent checks if any message contains banned keywords.
func (h *ComplianceHook) ContainsBannedContent(messages []map[string]interface{}) (bool, string) {
	for _, msg := range messages {
		if content, ok := msg["content"].(string); ok {
			lowerContent := strings.ToLower(content)
			for _, keyword := range h.BannedKeywords {
				if strings.Contains(lowerContent, strings.ToLower(keyword)) {
					return true, keyword
				}
			}
		}
	}
	return false, ""
}

// ServeHTTP implements the HTTP handler interface for external hook hosting.
//
// This shows how an external hook host would expose the hooks via HTTP:
//   - POST /hooks/v1/on_request → OnRequest
//   - POST /hooks/v1/on_response → OnResponse
//
// The host (externalhost.Host) handles:
//   - JSON encoding/decoding
//   - Timeout enforcement
//   - Error handling
//
// The hook implementation only needs to implement the business logic.
func (h *ComplianceHook) ServeHTTP(endpoint string, body []byte) ([]byte, error) {
	var inv contract.Invocation
	if err := json.Unmarshal(body, &inv); err != nil {
		return nil, fmt.Errorf("decode invocation: %w", err)
	}

	var reply *contract.Reply
	switch endpoint {
	case "/hooks/v1/on_request":
		reply = h.OnRequest(&inv)
	case "/hooks/v1/on_response":
		reply = h.OnResponse(&inv)
	default:
		return nil, fmt.Errorf("unknown endpoint: %s", endpoint)
	}

	return json.Marshal(reply)
}

// GetAuditReport returns a formatted compliance audit report.
func (h *ComplianceHook) GetAuditReport() string {
	var buf bytes.Buffer
	buf.WriteString("=== Compliance Audit Report ===\n")
	buf.WriteString(fmt.Sprintf("Total Requests: %d\n", len(h.AuditLog)))
	
	blocked := 0
	totalInput := 0
	totalOutput := 0
	for _, entry := range h.AuditLog {
		if entry.Blocked {
			blocked++
		}
		totalInput += entry.InputTokens
		totalOutput += entry.OutputTokens
	}
	
	buf.WriteString(fmt.Sprintf("Blocked: %d\n", blocked))
	buf.WriteString(fmt.Sprintf("Total Input Tokens: %d\n", totalInput))
	buf.WriteString(fmt.Sprintf("Total Output Tokens: %d\n", totalOutput))
	
	return buf.String()
}
