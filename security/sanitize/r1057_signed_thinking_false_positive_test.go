package sanitize

import (
	"context"
	"strings"
	"testing"
)

// TestR1057SignedThinkingFalsePositive verifies that signed thinking blocks
// are only rejected when they contain sensitive content that was sanitized,
// not merely because they have a signature field.
//
// Background: r1057 (2026-11-17) fixed a false-positive where any thinking
// block with a non-empty signature was rejected, even if it contained no
// sensitive information. The design intent (docs/design/2026-10-01-conversation-
// sanitization-cache-plan.md §3) is: signed content cannot be rewritten ⇒
// reject only when sensitive content is detected AND changed.
//
// 生产实证：211 在 2026-11-17 03:52:44 UTC 发生的报障（TraceID 9d236f12...，
// request c9bb3fbe...）——ZCode 客户端 158KB 请求被拒，日志显示 "invalid
// sanitizable request: sensitive signed thinking"，但请求本身不含敏感内容，
// 仅因 thinking 块带 signature 字段即被拒绝。
func TestR1057SignedThinkingFalsePositive(t *testing.T) {
	// 使用内部 API 直接测试 sanitizeContentBlock 的行为
	detector := NewPatternDetector()
	s, err := NewSanitizer(detector)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	tests := []struct {
		name       string
		blockJSON  string
		wantErr    bool
		wantChange bool
		desc       string
	}{
		{
			name: "signed thinking without sensitive content - should pass",
			blockJSON: `{
				"type": "thinking",
				"thinking": "I need to analyze this request carefully.",
				"signature": "sig_abc123"
			}`,
			wantErr:    false,
			wantChange: false,
			desc:       "Signed thinking with benign content should not be rejected",
		},
		{
			name: "signed thinking with password - should reject",
			blockJSON: `{
				"type": "thinking",
				"thinking": "password=MySecretPass123",
				"signature": "sig_abc123"
			}`,
			wantErr:    true,
			wantChange: false,
			desc:       "Signed thinking with sensitive content should be rejected (cannot rewrite signed content)",
		},
		{
			name: "unsigned thinking with password - should sanitize",
			blockJSON: `{
				"type": "thinking",
				"thinking": "password=MySecretPass123"
			}`,
			wantErr:    false,
			wantChange: true,
			desc:       "Unsigned thinking with sensitive content should be sanitized normally",
		},
		{
			name: "signed thinking with empty signature and password - should sanitize",
			blockJSON: `{
				"type": "thinking",
				"thinking": "password=MySecretPass123",
				"signature": ""
			}`,
			wantErr:    false,
			wantChange: true,
			desc:       "Empty signature is treated as unsigned, should sanitize normally",
		},
		{
			name: "text block with password - should sanitize",
			blockJSON: `{
				"type": "text",
				"text": "password=MySecretPass123"
			}`,
			wantErr:    false,
			wantChange: true,
			desc:       "Regular text blocks should sanitize normally",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitizer := &requestInputSanitizer{
				ctx:       ctx,
				sanitizer: s,
				offset:    make(map[SensitiveType]int),
				mapping:   make(SanitizeMap),
				usedCount: make(map[string]int),
				existing:  sanitizeReuseIndex{},
			}

			sanitized, changed, err := sanitizer.sanitizeContentBlock([]byte(tt.blockJSON))

			if tt.wantErr {
				if err == nil {
					t.Errorf("%s: expected error but got none", tt.desc)
				} else if !strings.Contains(err.Error(), "sensitive signed thinking") {
					t.Errorf("%s: got error %q, want sensitive signed thinking error", tt.desc, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tt.desc, err)
				}
			}

			if changed != tt.wantChange {
				t.Errorf("%s: changed=%v, want %v", tt.desc, changed, tt.wantChange)
			}

			// If we expect sanitization, verify placeholder was created
			if tt.wantChange && !tt.wantErr {
				if len(sanitizer.mapping) == 0 {
					t.Errorf("%s: expected sanitization but no placeholders created", tt.desc)
				}
				if sanitized == nil {
					t.Errorf("%s: expected sanitized output but got nil", tt.desc)
				}
			}
		})
	}
}

// TestR1057SignedThinkingProductionReplay replays the exact production incident
// from 2026-11-17 03:52:44 where a benign signed thinking block was rejected.
func TestR1057SignedThinkingProductionReplay(t *testing.T) {
	detector := NewPatternDetector()
	s, err := NewSanitizer(detector)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	// 简化的生产形态：ZCode 客户端发送的思考块，带 signature 但无敏感内容
	blockJSON := `{
		"type": "thinking",
		"thinking": "I need to check the git remote configuration and understand the deployment status. The current branch is main with commit f4f297b3d.",
		"signature": "sig_1a2b3c"
	}`

	sanitizer := &requestInputSanitizer{
		ctx:       ctx,
		sanitizer: s,
		offset:    make(map[SensitiveType]int),
		mapping:   make(SanitizeMap),
		usedCount: make(map[string]int),
		existing:  sanitizeReuseIndex{},
	}

	_, changed, err := sanitizer.sanitizeContentBlock([]byte(blockJSON))
	if err != nil {
		t.Fatalf("Production replay: benign signed thinking rejected: %v", err)
	}
	if changed {
		t.Error("Production replay: benign content should not trigger sanitization")
	}
	// When content is unchanged, sanitized may be the original or nil - both are acceptable
	// 无敏感内容，不应有映射生成
	if len(sanitizer.mapping) > 0 {
		t.Errorf("Production replay: unexpected sanitization: %d placeholders", len(sanitizer.mapping))
	}
}



