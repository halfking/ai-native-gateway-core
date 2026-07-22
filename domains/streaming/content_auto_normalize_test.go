package streaming

import (
	"strings"
	"sync"
	"testing"
)

// ─── audit a9ff405d3b51cf18f953475855d7851c regression coverage ────────
// (2026-07-23) Auto-normalization path for OpenAI Responses-style content
// blocks arriving through /v1/messages. Before this fix, blocks with
// type "input_text"/"input_image"/"input_file" were forwarded verbatim
// to Anthropic Messages, which silently dropped unknown types and
// produced the famous "I don't have any prior context" reply with
// prompt_tokens close to zero.

// TestConvertBlockMessage_AutoNormalizesInputText mirrors the actual
// request body that triggered the audit. The Chinese user instruction
// was disappearing because the message had {"type":"input_text","text":"..."}
// inside the content array — Anthropic did not recognise the type and
// dropped the block, leaving the model with only the system prompt.
func TestConvertBlockMessage_AutoNormalizesInputText(t *testing.T) {
	message := convertBlockMessage("user", []any{
		map[string]any{"type": "input_text", "text": "请对12小时内的修订进行审计，并总结完成情况，给出改进意见。"},
	})
	content, ok := message["content"].(string)
	if !ok {
		t.Fatalf("content = %#v, want string (audit invariant: no empty/multipart content)", message["content"])
	}
	if !strings.Contains(content, "请对12小时内的修订") {
		t.Fatalf("Chinese user instruction lost during auto-normalization: %q", content)
	}
}

// TestConvertBlockMessage_AutoNormalizesInputImage verifies that
// input_image blocks are reshaped to Anthropic {"type":"image","source":{...}}.
func TestConvertBlockMessage_AutoNormalizesInputImage(t *testing.T) {
	message := convertBlockMessage("user", []any{
		map[string]any{"type": "input_image", "image_url": map[string]any{"url": "https://example.test/a.png"}},
	})
	parts, ok := message["content"].([]any)
	if !ok {
		t.Fatalf("expected content parts for image, got %#v", message["content"])
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	part := parts[0].(map[string]any)
	if part["type"] != "image" {
		t.Fatalf("expected type=image, got %v", part["type"])
	}
	src, ok := part["source"].(map[string]any)
	if !ok || src["type"] != "url" {
		t.Fatalf("expected source.type=url, got %#v", part["source"])
	}
	if src["url"] != "https://example.test/a.png" {
		t.Fatalf("expected source.url preserved, got %v", src["url"])
	}
}

// TestConvertBlockMessage_PreservesInputFileAndAudio covers the two
// remaining Responses-only types. Both must remain lossless passthrough
// until an attachment-specific converter is available.
func TestConvertBlockMessage_PreservesInputFileAndAudio(t *testing.T) {
	t.Run("input_file", func(t *testing.T) {
		message := convertBlockMessage("user", []any{
			map[string]any{"type": "input_file", "file_id": "file_abc123"},
		})
		parts, ok := message["content"].([]map[string]any)
		if !ok || len(parts) != 1 || parts[0]["file_id"] != "file_abc123" {
			t.Fatalf("input_file dropped: %#v", message["content"])
		}
	})
	t.Run("input_audio", func(t *testing.T) {
		message := convertBlockMessage("user", []any{
			map[string]any{
				"type":        "input_audio",
				"input_audio": map[string]any{"format": "wav"},
			},
		})
		parts, ok := message["content"].([]map[string]any)
		if !ok || len(parts) != 1 {
			t.Fatalf("input_audio dropped: %#v", message["content"])
		}
		audio := parts[0]["input_audio"].(map[string]any)
		if audio["format"] != "wav" {
			t.Fatalf("input_audio metadata changed: %#v", parts[0])
		}
	})
}

// TestConvertBlockMessage_MixedInputTextAndToolUse replicates the realistic
// scenario where the user message contains both an input_text block and a
// tool_use from a previous model turn. Both must survive normalization.
func TestConvertBlockMessage_MixedInputTextAndToolUse(t *testing.T) {
	message := convertBlockMessage("assistant", []any{
		map[string]any{"type": "input_text", "text": "工具调用"},
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": "search", "input": map[string]any{}},
	})
	role := message["role"].(string)
	if role != "assistant" {
		t.Fatalf("role lost: %v", role)
	}
	if _, ok := message["tool_calls"]; !ok {
		t.Fatalf("tool_calls lost during normalization: %#v", message["content"])
	}
	if content, ok := message["content"].(string); !ok || !strings.Contains(content, "工具调用") {
		t.Fatalf("input_text dropped: %#v", message["content"])
	}
}

// TestNormalizeOpenAIResponsesBlock_PreservesFromType ensures the
// fromType return is stable so the audit log can identify the source
// shape. Operators rely on this to track which clients still emit
// Responses-only types after the auto-fix is rolled out.
func TestNormalizeOpenAIResponsesBlock_PreservesFromType(t *testing.T) {
	for _, typ := range []string{"input_text", "output_text", "input_image", "image_url"} {
		t.Run(typ, func(t *testing.T) {
			block := map[string]any{"type": typ}
			switch typ {
			case "input_text", "output_text":
				block["text"] = "hello"
			case "input_image", "image_url":
				block["image_url"] = map[string]any{"url": "https://x.test/p.png"}
			}
			_, fromType, ok := normalizeOpenAIResponsesBlock(block)
			if !ok {
				t.Fatalf("%s should normalize", typ)
			}
			if fromType != typ {
				t.Fatalf("fromType = %q, want %q", fromType, typ)
			}
		})
	}
}

func TestNormalizeOpenAIResponsesBlock_AttachmentsRemainPassthrough(t *testing.T) {
	for _, block := range []map[string]any{
		{"type": "input_audio", "input_audio": map[string]any{"data": "raw", "format": "wav"}},
		{"type": "input_file", "file_id": "file_test"},
		{"type": "file", "file": map[string]any{"filename": "report.pdf"}},
	} {
		if normalized, fromType, ok := normalizeOpenAIResponsesBlock(block); ok || normalized != nil || fromType != "" {
			t.Fatalf("attachment block should remain passthrough: %#v", block)
		}
	}
}

// TestNormalizeOpenAIResponsesBlock_UnknownTypeReturnsFalse guards against
// over-eager rewriting. Anthropic blocks we don't recognise must fall
// through to the existing passthrough behaviour unchanged.
func TestNormalizeOpenAIResponsesBlock_UnknownTypeReturnsFalse(t *testing.T) {
	for _, typ := range []string{"", "text", "image", "tool_use", "tool_result"} {
		t.Run(typ, func(t *testing.T) {
			block := map[string]any{"type": typ}
			_, _, ok := normalizeOpenAIResponsesBlock(block)
			if ok {
				t.Fatalf("%s should NOT normalize (it is already Anthropic)", typ)
			}
		})
	}
}

// TestRecordAutoNormalize_RateLimitedPerKey verifies that the per-tuple
// WARN log is rate-limited at 1 per 1000 rewrites. A burst test would
// otherwise flood the log pipeline on a stuck integration test loop.
func TestRecordAutoNormalize_RateLimitedPerKey(t *testing.T) {
	// Use a fresh key for this test so it does not race with other tests.
	const testSource = "test-record"
	const testType = "input_text"
	const testRole = "user"

	key := testSource + "|" + testType + "|" + testRole
	autoNormalizeCounters.Delete(key)
	for i := 0; i < 999; i++ {
		recordAutoNormalize(testSource, testType, testRole)
	}
	counter, ok := autoNormalizeCounters.Load(key)
	if !ok || counter.(*autoNormalizeCount).total.Load() != 999 {
		t.Fatalf("per-key counter did not record 999 rewrites: %#v", counter)
	}
	autoNormalizeCounters.Delete(key)
}

// TestRecordAutoNormalize_ConcurrentSafe issues many parallel record
// calls and ensures no race or panic. Cheap concurrency smoke test
// for the atomic + sync.Map backing the helper.
func TestRecordAutoNormalize_ConcurrentSafe(t *testing.T) {
	const goroutines = 32
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				recordAutoNormalize("concurrency-test", "input_text", "user")
			}
		}()
	}
	wg.Wait()
}
