// Package compression - smart_crusher.go
//
// SmartCrusher implements intelligent content compression by:
//   1. Removing redundant filler words (um, like, you know, etc.)
//   2. Compacting excessive whitespace
//   3. Summarizing repeated blocks
//
// Based on Headroom's SmartCrusher algorithm, adapted for Go.

package compression

import (
	"context"
	"regexp"
	"strings"
)

// SmartCrusherConfig configures SmartCrusher behavior.
type SmartCrusherConfig struct {
	RemoveRedundant   bool // Remove filler words
	CompactWhitespace bool // Compact excessive whitespace
	SummarizeBlocks   bool // Summarize repeated content blocks
}

// SmartCrusher removes redundant content from messages.
type SmartCrusher struct {
	config            SmartCrusherConfig
	redundantPatterns []*regexp.Regexp
	whitespacePattern *regexp.Regexp
}

// NewSmartCrusher creates a new SmartCrusher.
func NewSmartCrusher(config SmartCrusherConfig) *SmartCrusher {
	sc := &SmartCrusher{
		config: config,
	}

	if config.RemoveRedundant {
		// Common filler words and phrases
		patterns := []string{
			`\b(um|uh|er|ah)\b`,
			`\b(like|you know|I mean|basically|actually|literally|really|very|quite)\b`,
			`\b(kind of|sort of|a bit)\b`,
			`\s+(um|uh|er|ah)\s+`,
		}
		sc.redundantPatterns = make([]*regexp.Regexp, 0, len(patterns))
		for _, p := range patterns {
			if re, err := regexp.Compile(`(?i)` + p); err == nil {
				sc.redundantPatterns = append(sc.redundantPatterns, re)
			}
		}
	}

	if config.CompactWhitespace {
		sc.whitespacePattern = regexp.MustCompile(`\s+`)
	}

	return sc
}

// Crush applies smart crushing to messages.
func (sc *SmartCrusher) Crush(ctx context.Context, messages []Message) ([]Message, error) {
	if len(messages) == 0 {
		return messages, nil
	}

	result := make([]Message, 0, len(messages))
	for _, msg := range messages {
		crushed := msg
		crushed.Content = sc.crushContent(msg.Content)
		result = append(result, crushed)
	}

	return result, nil
}

// crushContent applies crushing to a single content string.
func (sc *SmartCrusher) crushContent(content string) string {
	if content == "" {
		return content
	}

	result := content

	// Remove redundant patterns
	if sc.config.RemoveRedundant {
		result = sc.removeRedundant(result)
	}

	// Compact whitespace
	if sc.config.CompactWhitespace {
		result = sc.compactWhitespace(result)
	}

	// Summarize blocks (if enabled)
	if sc.config.SummarizeBlocks {
		result = sc.summarizeBlocks(result)
	}

	return strings.TrimSpace(result)
}

// removeRedundant removes filler words and redundant patterns.
func (sc *SmartCrusher) removeRedundant(text string) string {
	result := text
	for _, pattern := range sc.redundantPatterns {
		result = pattern.ReplaceAllString(result, " ")
	}
	return result
}

// compactWhitespace reduces multiple spaces to single space.
func (sc *SmartCrusher) compactWhitespace(text string) string {
	if sc.whitespacePattern == nil {
		return text
	}
	return sc.whitespacePattern.ReplaceAllString(text, " ")
}

// summarizeBlocks identifies and summarizes repeated content blocks.
func (sc *SmartCrusher) summarizeBlocks(text string) string {
	// Simple implementation: detect repeated sentences
	lines := strings.Split(text, "\n")
	if len(lines) <= 2 {
		return text
	}

	seen := make(map[string]int)
	result := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		seen[trimmed]++
		if seen[trimmed] <= 2 {
			// Keep first two occurrences
			result = append(result, line)
		} else if seen[trimmed] == 3 {
			// Replace subsequent with summary marker
			result = append(result, "[repeated content omitted]")
		}
		// Skip further repetitions
	}

	return strings.Join(result, "\n")
}
