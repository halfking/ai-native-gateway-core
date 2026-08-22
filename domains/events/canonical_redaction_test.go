package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCanonicalTypesRedaction 验证三个 canonical 类型序列化后不含高敏 secret 模式。
// 这是 02-CROSS-REPO-EVENT-CONTRACT.md §3 的红线：任何含 prompt/response/
// token/credential 的 payload 都必须被拒绝。本测试锁定字段白名单本身，
// 防止后续给这些类型加入能承载 secret 的字段。
func TestCanonicalTypesRedaction(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	pid := int64(42)

	// 用合法的低敏值填充所有字段。如果序列化结果里出现 secret 模式，
	// 说明字段命名或值本身泄露了敏感语义。
	provider := ProviderRef{
		CatalogCode:     "anthropic",
		ProviderID:      &pid,
		Protocol:        "anthropic-messages",
		Tier:            "tier1",
		VendorName:      "Anthropic",
		CredentialID:    &pid,
		CredentialLabel: "prod-label",
	}
	decision := RoutingDecision{
		Strategy:        "p2c",
		DecisionVersion: DecisionVersionV1,
		Provider:        provider,
		ExplanationCode: "p2c_low_penalty",
		Timestamp:       now,
	}
	compression := CompressionEvent{
		Mode:        "lite",
		Stages:      []string{"whitespace", "system-dedup"},
		InputChars:  1000,
		OutputChars: 800,
		SavedChars:  200,
		ReasonCode:  "context_headroom",
		Timestamp:   now,
	}

	// 高敏模式：任意 canonical 类型的 JSON 都不得包含这些子串。
	forbiddenSubstrings := []string{
		"api_key", "apikey",
		"bearer",
		"secret",
		"password",
		"token",      // credential token / access token
		"prompt",     // prompt 正文
		"session_id", // 高基数，不进 canonical payload
		"request_id",
		"tenant_id", // signed tenant 不得进 payload 覆盖
		"/users/",   // 绝对路径
		"sk-",       // OpenAI key 前缀
	}

	cases := []struct {
		name string
		v    any
	}{
		{"ProviderRef", provider},
		{"RoutingDecision", decision},
		{"CompressionEvent", compression},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			lower := strings.ToLower(string(raw))
			for _, bad := range forbiddenSubstrings {
				if strings.Contains(lower, bad) {
					t.Errorf("%s JSON contains forbidden substring %q\njson: %s", tc.name, bad, raw)
				}
			}
		})
	}
}

// TestCanonicalTypesFieldWhitelist 锁定字段集合，防止悄悄加入 secret-bearing 字段。
// 维护字段白名单时必须同步更新这里的预期 key 列表。
func TestCanonicalTypesFieldWhitelist(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		v       any
		allowed map[string]bool
	}{
		{
			name: "ProviderRef",
			v:    ProviderRef{},
			allowed: map[string]bool{
				"catalog_code": true, "provider_id": true, "protocol": true,
				"tier": true, "vendor_name": true,
				"credential_id": true, "credential_label": true,
			},
		},
		{
			name: "RoutingDecision",
			v:    RoutingDecision{},
			allowed: map[string]bool{
				"strategy": true, "decision_version": true,
				"provider": true, "explanation_code": true, "timestamp": true,
			},
		},
		{
			name: "CompressionEvent",
			v:    CompressionEvent{},
			allowed: map[string]bool{
				"mode": true, "stages": true,
				"input_chars": true, "output_chars": true, "saved_chars": true,
				"reason_code": true, "timestamp": true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}
			for k := range m {
				if !tc.allowed[k] {
					t.Errorf("%s has unexpected JSON key %q (not in whitelist); "+
						"if this is intentional, update the allowed list AND the redaction test", tc.name, k)
				}
			}
		})
	}
}
