package v2

import (
	"strings"
)

// SubmitMode represents how the client submitted the request
type SubmitMode string

const (
	SubmitModeFull               SubmitMode = "full"                // Client sent full history
	SubmitModeDelta              SubmitMode = "delta"               // Client sent only new messages
	SubmitModeSnapshot           SubmitMode = "snapshot"            // Client sent compressed snapshot
	SubmitModeInferredCompressed SubmitMode = "inferred_compressed" // Gateway inferred client compressed
)

// SubmitModeDetector detects how the client submitted the request
//
// This uses multiple signals to determine if the client sent:
//   - Full message history (normal mode)
//   - Only delta (incremental mode)
//   - Pre-compressed snapshot
//   - Inferred compression (gateway detects client already compressed)
//
// Priority order (high to low):
//   P0: X-Gw-Submit-Mode header (explicit)
//   P1: Message count regression (compressed)
//   P2: Summary marker detection (compressed)
//   P3: Orphaned tool_result (compressed)
//   P4: LCS stable prefix (full)
//   P5: No previous outbound (first turn)
type SubmitModeDetector struct {
	// Configuration
	LCSThreshold float64 // Minimum overlap ratio to consider "full" mode (default 0.7)
}

// NewSubmitModeDetector creates a new detector with default settings
func NewSubmitModeDetector() *SubmitModeDetector {
	return &SubmitModeDetector{
		LCSThreshold: 0.7,
	}
}

// DetectionContext contains the context needed for detection
type DetectionContext struct {
	// Request headers
	SubmitModeHeader string // X-Gw-Submit-Mode header value

	// Message content
	ClientMessages   []Message // Messages from client
	LastOutboundBody []Message // Previous turn's outbound body

	// Compression metadata
	CompressionApplied bool
}

// Detect detects the submit mode based on multiple signals
func (d *SubmitModeDetector) Detect(ctx DetectionContext) SubmitMode {
	// P0: Check explicit header (highest priority)
	if mode := d.checkHeader(ctx.SubmitModeHeader); mode != "" {
		return mode
	}

	// P5: First turn or no previous context
	if len(ctx.LastOutboundBody) == 0 {
		return SubmitModeFull
	}

	// P1: Message count regression (client sent fewer messages than last outbound)
	if d.checkMessageCountRegression(ctx) {
		return SubmitModeInferredCompressed
	}

	// P2: Summary marker detection
	if d.checkSummaryMarker(ctx.ClientMessages) {
		return SubmitModeInferredCompressed
	}

	// P3: Orphaned tool_result detection
	if d.checkOrphanedToolResult(ctx) {
		return SubmitModeInferredCompressed
	}

	// P4: LCS (Longest Common Subsequence) analysis
	overlap := d.calculateLCSOverlap(ctx.ClientMessages, ctx.LastOutboundBody)
	if overlap >= d.LCSThreshold {
		// High overlap means client sent full history
		return SubmitModeFull
	}

	// Low overlap but not caught by other signals: likely inferred compressed
	if overlap < 0.3 {
		return SubmitModeInferredCompressed
	}

	// Default: treat as full mode
	return SubmitModeFull
}

// checkHeader checks the X-Gw-Submit-Mode header
func (d *SubmitModeDetector) checkHeader(header string) SubmitMode {
	switch strings.ToLower(strings.TrimSpace(header)) {
	case "delta":
		return SubmitModeDelta
	case "snapshot":
		return SubmitModeSnapshot
	case "full":
		return SubmitModeFull
	default:
		return ""
	}
}

// checkMessageCountRegression checks if message count decreased significantly
func (d *SubmitModeDetector) checkMessageCountRegression(ctx DetectionContext) bool {
	clientCount := len(ctx.ClientMessages)
	lastOutboundCount := len(ctx.LastOutboundBody)

	// If client sent significantly fewer messages, likely compressed
	if clientCount < lastOutboundCount {
		// Also check LCS overlap to avoid false positives
		overlap := d.calculateLCSOverlap(ctx.ClientMessages, ctx.LastOutboundBody)
		if overlap < 0.3 {
			return true
		}
	}

	return false
}

// checkSummaryMarker checks if messages contain summary markers
func (d *SubmitModeDetector) checkSummaryMarker(messages []Message) bool {
	for _, msg := range messages {
		// Check for [smm_v1:...] or similar summary markers
		if strings.Contains(msg.Content, "[smm_v1:") ||
			strings.Contains(msg.Content, "[summary:") ||
			strings.Contains(msg.Content, "[compressed:") {
			return true
		}
	}
	return false
}

// checkOrphanedToolResult checks for tool_call_id without corresponding tool_calls
func (d *SubmitModeDetector) checkOrphanedToolResult(ctx DetectionContext) bool {
	// Build set of tool_call_ids from tool_calls in current messages
	toolCallIDs := make(map[string]bool)
	for _, msg := range ctx.ClientMessages {
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if id, ok := tc["id"].(string); ok {
					toolCallIDs[id] = true
				}
			}
		}
	}

	// Check for orphaned tool results
	for _, msg := range ctx.ClientMessages {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			if !toolCallIDs[msg.ToolCallID] {
				// Found tool result without corresponding tool call
				// This indicates the tool_calls message was compressed out
				return true
			}
		}
	}

	return false
}

// calculateLCSOverlap calculates the overlap ratio between two message arrays
//
// Returns a value between 0.0 (no overlap) and 1.0 (full overlap).
// This uses a simplified LCS-like algorithm optimized for message arrays.
func (d *SubmitModeDetector) calculateLCSOverlap(client, lastOutbound []Message) float64 {
	if len(client) == 0 || len(lastOutbound) == 0 {
		return 0.0
	}

	// Build a map of message fingerprints from lastOutbound
	lastOutboundSet := make(map[string]bool)
	for _, msg := range lastOutbound {
		fingerprint := messageFingerprint(msg)
		lastOutboundSet[fingerprint] = true
	}

	// Count how many client messages match lastOutbound
	matchCount := 0
	for _, msg := range client {
		fingerprint := messageFingerprint(msg)
		if lastOutboundSet[fingerprint] {
			matchCount++
		}
	}

	// Calculate overlap ratio
	// We compare against the smaller array to avoid penalizing growth
	denominator := min(len(client), len(lastOutbound))
	if denominator == 0 {
		return 0.0
	}

	return float64(matchCount) / float64(denominator)
}

// messageFingerprint generates a fingerprint for a message
//
// This is used for quick comparison without exact equality.
// Uses: role + first 200 chars of content + tool_call_id (if present)
func messageFingerprint(msg Message) string {
	content := msg.Content
	if len(content) > 200 {
		content = content[:200]
	}

	fingerprint := msg.Role + ":" + content

	if msg.ToolCallID != "" {
		fingerprint += ":tool:" + msg.ToolCallID
	}

	if len(msg.ToolCalls) > 0 {
		fingerprint += ":calls:" + string(rune(len(msg.ToolCalls)))
	}

	return fingerprint
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DetectWithRequest is a convenience method that extracts detection context from ProcessedRequest
func (d *SubmitModeDetector) DetectWithRequest(req *ProcessedRequest, header string) SubmitMode {
	return d.Detect(DetectionContext{
		SubmitModeHeader:   header,
		ClientMessages:     req.RequestBody,
		LastOutboundBody:   req.LastOutboundBody,
		CompressionApplied: req.CompressionApplied,
	})
}
