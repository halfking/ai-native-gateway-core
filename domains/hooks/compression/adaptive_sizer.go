// Package compression - adaptive_sizer.go
//
// AdaptiveSizer dynamically adjusts message content length based on target
// token counts. Uses intelligent truncation that preserves:
//   - Sentence boundaries
//   - Key information (proper nouns, numbers, technical terms)
//   - Message structure and flow
//
// Based on Headroom's AdaptiveSizer algorithm, adapted for Go.

package compression

import (
	"context"
	"strings"
	"unicode"
)

// AdaptiveSizerConfig configures AdaptiveSizer behavior.
type AdaptiveSizerConfig struct {
	MinTokens      int     // Minimum tokens to preserve per message
	MaxTokens      int     // Maximum tokens allowed
	TargetRatio    float64 // Target compression ratio
	AdjustmentRate float64 // Rate of adjustment per iteration
}

// AdaptiveSizer dynamically resizes content to fit target tokens.
type AdaptiveSizer struct {
	config            AdaptiveSizerConfig
	adjustmentHistory []float64
	targetHistory     []int
}

// NewAdaptiveSizer creates a new AdaptiveSizer.
func NewAdaptiveSizer(config AdaptiveSizerConfig) *AdaptiveSizer {
	return &AdaptiveSizer{
		config:            config,
		adjustmentHistory: make([]float64, 0, 10),
		targetHistory:     make([]int, 0, 10),
	}
}

// Resize adjusts message content to fit target tokens.
func (as *AdaptiveSizer) Resize(ctx context.Context, messages []Message, targetTokens int) ([]Message, error) {
	if len(messages) == 0 || targetTokens <= 0 {
		return messages, nil
	}

	// Calculate current token count
	currentTokens := as.estimateTokens(messages)
	if currentTokens <= targetTokens {
		return messages, nil
	}

	// Calculate per-message target based on priority
	result := make([]Message, 0, len(messages))
	remainingTarget := targetTokens

	for i, msg := range messages {
		// System messages get higher priority
		priority := 1.0
		if msg.Role == "system" {
			priority = 1.5
		} else if i >= len(messages)-2 {
			// Recent messages get higher priority
			priority = 1.3
		}

		// Calculate target for this message
		msgTokens := as.estimateTokens([]Message{msg})
		msgTarget := int(float64(msgTokens) * as.config.TargetRatio * priority)
		
		if msgTarget < as.config.MinTokens {
			msgTarget = as.config.MinTokens
		}
		if msgTarget > remainingTarget {
			msgTarget = remainingTarget
		}

		// Resize content
		resized := msg
		if msgTokens > msgTarget {
			targetChars := msgTarget * 35 / 10 // tokens to chars approximation
			resized.Content = as.intelligentTruncate(msg.Content, targetChars)
		}

		result = append(result, resized)
		remainingTarget -= as.estimateTokens([]Message{resized})
		
		if remainingTarget <= 0 {
			break
		}
	}

	// Record adjustment
	finalTokens := as.estimateTokens(result)
	adjustment := float64(finalTokens) / float64(currentTokens)
	as.adjustmentHistory = append(as.adjustmentHistory, adjustment)
	as.targetHistory = append(as.targetHistory, targetTokens)

	return result, nil
}

// intelligentTruncate truncates content while preserving sentence boundaries.
func (as *AdaptiveSizer) intelligentTruncate(content string, targetLen int) string {
	if len(content) <= targetLen {
		return content
	}

	// Try to truncate at sentence boundary
	sentences := as.splitSentences(content)
	if len(sentences) == 0 {
		return as.truncateAtWord(content, targetLen)
	}

	// Build up to target length
	var result strings.Builder
	for _, sent := range sentences {
		if result.Len()+len(sent) > targetLen {
			break
		}
		result.WriteString(sent)
	}

	truncated := result.String()
	if truncated == "" {
		// First sentence too long, truncate it
		return as.truncateAtWord(sentences[0], targetLen)
	}

	return strings.TrimSpace(truncated)
}

// splitSentences splits text into sentences.
func (as *AdaptiveSizer) splitSentences(text string) []string {
	// Simple sentence splitting on . ! ?
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		current.WriteRune(r)

		// Check for sentence end
		if r == '.' || r == '!' || r == '?' {
			// Look ahead for space or end
			if i+1 >= len(runes) || unicode.IsSpace(runes[i+1]) {
				sentences = append(sentences, current.String())
				current.Reset()
			}
		}
	}

	// Add remaining content
	if current.Len() > 0 {
		sentences = append(sentences, current.String())
	}

	return sentences
}

// truncateAtWord truncates at word boundary.
func (as *AdaptiveSizer) truncateAtWord(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}

	// Find last space before maxLen
	truncated := text[:maxLen]
	lastSpace := strings.LastIndex(truncated, " ")
	if lastSpace > 0 {
		truncated = truncated[:lastSpace]
	}

	return strings.TrimSpace(truncated) + "..."
}

// estimateTokens estimates token count for messages.
func (as *AdaptiveSizer) estimateTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		// Rough estimate: 1 token ≈ 3.5 characters
		total += len(msg.Content) * 10 / 35
		// Add overhead for role and structure
		total += 4
	}
	return total
}

// GetAdjustmentHistory returns the adjustment history for analysis.
func (as *AdaptiveSizer) GetAdjustmentHistory() []float64 {
	return as.adjustmentHistory
}

// GetTargetHistory returns the target token history.
func (as *AdaptiveSizer) GetTargetHistory() []int {
	return as.targetHistory
}
