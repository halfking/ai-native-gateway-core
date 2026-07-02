// Package compression - headroom_compressor.go
//
// HeadroomCompressor integrates Headroom token compression algorithms into
// the LLM Gateway compression pipeline. Based on Headroom's SmartCrusher
// and AdaptiveSizer, this provides intelligent redundancy removal and
// adaptive content sizing while preserving critical information.
//
// Key features:
//   - SmartCrusher: removes filler words, compacts whitespace, summarizes blocks
//   - AdaptiveSizer: dynamically adjusts content length based on target tokens
//   - CompressionLog: tracks before/after state for session stitching
//   - StitchSession: combines original and compressed messages with metadata
//
// Design note: HeadroomCompressor operates on []map[string]any (the same shape
// the rebuilder/diff helpers use) so it round-trips non-content fields such as
// tool_calls, tool_call_id, name, etc. without dropping them.

package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// HeadroomConfig holds configuration for Headroom compression.
type HeadroomConfig struct {
	TargetRatio         float64 // Target compression ratio (0.0-1.0)
	MaxTokens           int     // Maximum tokens allowed
	EnableSmartCrusher  bool    // Enable SmartCrusher for redundancy removal
	EnableAdaptiveSizer bool    // Enable AdaptiveSizer for dynamic sizing
	PreserveSystem      bool    // Preserve system messages
	PreserveLastN       int     // Preserve last N messages unchanged
}

// CompressionLog records compression state before and after. It is written
// into SessionState so the diff layer (diff.go) and downstream consumers can
// observe what Headroom did to the conversation.
type CompressionLog struct {
	SessionID          string    `json:"session_id"`
	OriginalMessages   []Message `json:"original_messages"`
	CompressedMessages []Message `json:"compressed_messages"`
	CompressionRatio   float64   `json:"compression_ratio"`
	TokensSaved        int       `json:"tokens_saved"`
	Timestamp          time.Time `json:"timestamp"`
	Strategy           string    `json:"strategy"`
}

// HeadroomCompressor is the main Headroom compression implementation.
// It is constructed per-compression by tryHeadroomCompression and is NOT
// safe for concurrent reuse (the CompressionLog is mutated in place).
type HeadroomCompressor struct {
	config         HeadroomConfig
	smartCrusher   *SmartCrusher
	adaptiveSizer  *AdaptiveSizer
	compressionLog *CompressionLog
}

// NewHeadroomCompressor creates a new HeadroomCompressor.
func NewHeadroomCompressor(config HeadroomConfig) (*HeadroomCompressor, error) {
	hc := &HeadroomCompressor{
		config: config,
		compressionLog: &CompressionLog{
			Timestamp: time.Now(),
		},
	}

	if config.EnableSmartCrusher {
		hc.smartCrusher = NewSmartCrusher(SmartCrusherConfig{
			RemoveRedundant:   true,
			CompactWhitespace: true,
			SummarizeBlocks:   false,
		})
	}

	if config.EnableAdaptiveSizer {
		hc.adaptiveSizer = NewAdaptiveSizer(AdaptiveSizerConfig{
			MinTokens:      100,
			MaxTokens:      config.MaxTokens,
			TargetRatio:    config.TargetRatio,
			AdjustmentRate: 0.1,
		})
	}

	return hc, nil
}

// Compress compresses messages using Headroom algorithms.
//
// The input is []map[string]any (the same shape extractMessages yields after
// json.Unmarshal). Non-content fields (tool_calls, name, tool_call_id, …) are
// preserved verbatim — only the "content" string is rewritten by SmartCrusher /
// AdaptiveSizer. Output order matches input order.
func (hc *HeadroomCompressor) Compress(ctx context.Context, messages []map[string]interface{}, targetTokens int) ([]map[string]interface{}, error) {
	if len(messages) == 0 {
		return messages, nil
	}

	// Build the original-message view for the CompressionLog.
	originalMsgs := make([]Message, 0, len(messages))
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		originalMsgs = append(originalMsgs, Message{Role: role, Content: content})
	}
	hc.compressionLog.OriginalMessages = originalMsgs
	originalTokens := estimateMessagesTokens(originalMsgs)

	// Decide which indices to compress vs. preserve. We keep the slice
	// positions so the output stays in the original conversation order.
	preserve := make([]bool, len(messages))
	for i, msg := range messages {
		role, _ := msg["role"].(string)
		if hc.config.PreserveSystem && role == "system" {
			preserve[i] = true
		}
	}
	// Preserve last N messages (system or not).
	if hc.config.PreserveLastN > 0 && hc.config.PreserveLastN < len(messages) {
		for i := len(messages) - hc.config.PreserveLastN; i < len(messages); i++ {
			preserve[i] = true
		}
	}

	// Gather the to-compress subset in order.
	var toCompress []Message
	for i, msg := range messages {
		if preserve[i] {
			continue
		}
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		toCompress = append(toCompress, Message{Role: role, Content: content})
	}

	compressed := toCompress
	strategy := "none"

	if hc.smartCrusher != nil && len(compressed) > 0 {
		if crushed, err := hc.smartCrusher.Crush(ctx, compressed); err == nil {
			compressed = crushed
			strategy = "smart_crusher"
		}
	}

	if hc.adaptiveSizer != nil && targetTokens > 0 && len(compressed) > 0 {
		if resized, err := hc.adaptiveSizer.Resize(ctx, compressed, targetTokens); err == nil {
			compressed = resized
			if strategy == "smart_crusher" {
				strategy = "smart_crusher+adaptive_sizer"
			} else {
				strategy = "adaptive_sizer"
			}
		}
	}

	// Re-assemble output in original order, copying each original map and
	// overwriting "content" only for the indices we compressed. This keeps
	// every other field (tool_calls, tool_call_id, name, …) intact.
	output := make([]map[string]interface{}, len(messages))
	compressedViews := make([]Message, 0, len(messages))
	compIdx := 0
	for i, msg := range messages {
		copyMap := make(map[string]interface{}, len(msg))
		for k, v := range msg {
			copyMap[k] = v
		}
		if preserve[i] {
			role, _ := msg["role"].(string)
			content, _ := msg["content"].(string)
			compressedViews = append(compressedViews, Message{Role: role, Content: content})
		} else if compIdx < len(compressed) {
			copyMap["content"] = compressed[compIdx].Content
			compressedViews = append(compressedViews, compressed[compIdx])
			compIdx++
		} else {
			role, _ := msg["role"].(string)
			content, _ := msg["content"].(string)
			compressedViews = append(compressedViews, Message{Role: role, Content: content})
		}
		output[i] = copyMap
	}

	// Update the compression log.
	hc.compressionLog.CompressedMessages = compressedViews
	hc.compressionLog.Strategy = strategy
	compressedTokens := estimateMessagesTokens(compressedViews)
	hc.compressionLog.TokensSaved = originalTokens - compressedTokens
	if originalTokens > 0 {
		hc.compressionLog.CompressionRatio = float64(compressedTokens) / float64(originalTokens)
	}

	return output, nil
}

// GetCompressionLog returns the compression log.
func (hc *HeadroomCompressor) GetCompressionLog() CompressionLog {
	if hc.compressionLog == nil {
		return CompressionLog{}
	}
	return *hc.compressionLog
}

// StitchSession combines compressed messages with a metadata preamble.
// It does NOT mutate the receiver's log (the returned messages are a new slice).
func (hc *HeadroomCompressor) StitchSession(sessionID string) ([]Message, error) {
	if hc.compressionLog == nil {
		return nil, fmt.Errorf("no compression log available")
	}

	log := hc.compressionLog
	metaContent := fmt.Sprintf("Compression applied: %s\nOriginal messages: %d\nCompressed messages: %d\nTokens saved: %d\nCompression ratio: %.2f",
		log.Strategy,
		len(log.OriginalMessages),
		len(log.CompressedMessages),
		log.TokensSaved,
		log.CompressionRatio)

	result := make([]Message, 0, 1+len(log.CompressedMessages))
	result = append(result, Message{Role: "system", Content: metaContent})
	result = append(result, log.CompressedMessages...)
	return result, nil
}

// estimateMessagesTokens estimates the token count for a slice of messages.
// Rough heuristic: 1 token ≈ 3.5 characters, +4 tokens overhead per message.
func estimateMessagesTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) * 10 / 35
		total += 4
	}
	return total
}

// Marshal serializes the compression log to JSON for telemetry storage.
func (log *CompressionLog) Marshal() []byte {
	b, _ := json.Marshal(log)
	return b
}
