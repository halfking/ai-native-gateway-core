package compression

import (
	"context"
	"testing"
)

func TestHeadroomCompressor_BasicCompression(t *testing.T) {
	config := HeadroomConfig{
		TargetRatio:         0.5,
		MaxTokens:           2000,
		EnableSmartCrusher:  true,
		EnableAdaptiveSizer: true,
		PreserveSystem:      true,
		PreserveLastN:       2,
	}

	compressor, err := NewHeadroomCompressor(config)
	if err != nil {
		t.Fatalf("Failed to create compressor: %v", err)
	}

	// Test messages
	messages := []map[string]interface{}{
		{
			"role":    "system",
			"content": "You are a helpful assistant.",
		},
		{
			"role":    "user",
			"content": "This is a very long message with lots of um, redundant content. Like, you know, it has basically quite very many filler words that can be removed. Actually, this message is really quite unnecessarily verbose and could be compressed significantly.",
		},
		{
			"role":    "assistant",
			"content": "I understand. Let me help you with that. This is also a long response with some repetition. This is also a long response with some repetition.",
		},
		{
			"role":    "user",
			"content": "Can you summarize the key points?",
		},
	}

	ctx := context.Background()
	targetTokens := 500

	compressed, err := compressor.Compress(ctx, messages, targetTokens)
	if err != nil {
		t.Fatalf("Compression failed: %v", err)
	}

	// Verify compression happened
	if len(compressed) == 0 {
		t.Fatal("Compressed messages should not be empty")
	}

	// Verify system message preserved
	if compressed[0]["role"] != "system" {
		t.Error("System message should be preserved")
	}

	// Verify compression log
	log := compressor.GetCompressionLog()
	if log.CompressionRatio >= 1.0 {
		t.Errorf("Expected compression ratio < 1.0, got %.2f", log.CompressionRatio)
	}

	if log.TokensSaved <= 0 {
		t.Errorf("Expected tokens saved > 0, got %d", log.TokensSaved)
	}

	t.Logf("Compression successful:")
	t.Logf("  Original messages: %d", len(messages))
	t.Logf("  Compressed messages: %d", len(compressed))
	t.Logf("  Compression ratio: %.2f", log.CompressionRatio)
	t.Logf("  Tokens saved: %d", log.TokensSaved)
	t.Logf("  Strategy: %s", log.Strategy)
}

func TestSmartCrusher_RemoveRedundant(t *testing.T) {
	config := SmartCrusherConfig{
		RemoveRedundant:   true,
		CompactWhitespace: true,
		SummarizeBlocks:   false,
	}

	crusher := NewSmartCrusher(config)

	messages := []Message{
		{
			Role:    "user",
			Content: "Um, like, I was wondering if, you know, you could help me with this task. Actually, it's really quite important.",
		},
	}

	ctx := context.Background()
	crushed, err := crusher.Crush(ctx, messages)
	if err != nil {
		t.Fatalf("Crushing failed: %v", err)
	}

	if len(crushed) == 0 {
		t.Fatal("Crushed messages should not be empty")
	}

	// Verify filler words removed
	content := crushed[0].Content
	if len(content) >= len(messages[0].Content) {
		t.Error("Content should be shorter after crushing")
	}

	t.Logf("Smart crushing successful:")
	t.Logf("  Original: %s", messages[0].Content)
	t.Logf("  Crushed: %s", content)
}

func TestAdaptiveSizer_Resize(t *testing.T) {
	config := AdaptiveSizerConfig{
		MinTokens:      100,
		MaxTokens:      1000,
		TargetRatio:    0.5,
		AdjustmentRate: 0.1,
	}

	sizer := NewAdaptiveSizer(config)

	// Create long message
	longContent := "This is a very long message that needs to be resized. "
	for i := 0; i < 50; i++ {
		longContent += "Lorem ipsum dolor sit amet consectetur adipiscing elit. "
	}

	messages := []Message{
		{
			Role:    "user",
			Content: longContent,
		},
	}

	ctx := context.Background()
	targetTokens := 150 // Set target lower than original to force resize

	resized, err := sizer.Resize(ctx, messages, targetTokens)
	if err != nil {
		t.Fatalf("Resizing failed: %v", err)
	}

	if len(resized) == 0 {
		t.Fatal("Resized messages should not be empty")
	}

	// Verify size reduction
	originalTokens := sizer.estimateTokens(messages)
	resizedTokens := sizer.estimateTokens(resized)

	if resizedTokens >= originalTokens {
		t.Errorf("Expected size reduction, got original=%d, resized=%d", originalTokens, resizedTokens)
	}

	t.Logf("Adaptive sizing successful:")
	t.Logf("  Original tokens: %d", originalTokens)
	t.Logf("  Resized tokens: %d", resizedTokens)
	t.Logf("  Target tokens: %d", targetTokens)
	t.Logf("  Ratio: %.2f", float64(resizedTokens)/float64(originalTokens))
}

func TestHeadroomCompressor_SessionStitching(t *testing.T) {
	config := HeadroomConfig{
		TargetRatio:         0.6,
		MaxTokens:           1000,
		EnableSmartCrusher:  true,
		EnableAdaptiveSizer: false,
	}

	compressor, err := NewHeadroomCompressor(config)
	if err != nil {
		t.Fatalf("Failed to create compressor: %v", err)
	}

	messages := []map[string]interface{}{
		{"role": "user", "content": "Original message 1"},
		{"role": "assistant", "content": "Response 1"},
		{"role": "user", "content": "Original message 2"},
	}

	ctx := context.Background()
	_, err = compressor.Compress(ctx, messages, 500)
	if err != nil {
		t.Fatalf("Compression failed: %v", err)
	}

	// Test session stitching
	stitched, err := compressor.StitchSession("test-session-123")
	if err != nil {
		t.Fatalf("Session stitching failed: %v", err)
	}

	// Verify stitched session has metadata
	if len(stitched) == 0 {
		t.Fatal("Stitched session should not be empty")
	}

	// First message should be compression metadata
	if stitched[0].Role != "system" {
		t.Error("First message should be compression metadata")
	}

	t.Logf("Session stitching successful:")
	t.Logf("  Stitched messages: %d", len(stitched))
	t.Logf("  Metadata: %s", stitched[0].Content)
}
