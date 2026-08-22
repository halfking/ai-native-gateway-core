package streaming

import "testing"

func TestExtractPreferredCredential_Header(t *testing.T) {
	adminKey := "ops-admin-token"
	// header + admin token → override applied
	got := ExtractPreferredCredential("42", nil, adminKey, adminKey)
	if got != "42" {
		t.Errorf("header override: expected \"42\", got %q", got)
	}
}

func TestExtractPreferredCredential_NonAdminHeaderIgnored(t *testing.T) {
	adminKey := "ops-admin-token"
	// header from a non-admin caller → silently dropped (must not leak
	// routing control to ordinary client keys).
	got := ExtractPreferredCredential("42", nil, "client-api-key", adminKey)
	if got != "" {
		t.Errorf("non-admin header: expected empty, got %q", got)
	}
}

func TestExtractPreferredCredential_EmptyAdminKeyDisabled(t *testing.T) {
	// No admin token configured → feature disabled entirely.
	got := ExtractPreferredCredential("42", nil, "client", "")
	if got != "" {
		t.Errorf("empty admin key: expected empty, got %q", got)
	}
}

func TestExtractPreferredCredential_BodyJSON(t *testing.T) {
	adminKey := "ops-admin-token"
	body := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}],"metadata":{"preferred_credential":"42"}}`)
	got := ExtractPreferredCredential("", body, adminKey, adminKey)
	if got != "42" {
		t.Errorf("body metadata override: expected \"42\", got %q", got)
	}
}

func TestExtractPreferredCredential_BodyNumeric(t *testing.T) {
	adminKey := "ops-admin-token"
	// Numeric literal in JSON (no quotes) is tolerated.
	body := []byte(`{"metadata":{"preferred_credential":42}}`)
	got := ExtractPreferredCredential("", body, adminKey, adminKey)
	if got != "42" {
		t.Errorf("numeric body override: expected \"42\", got %q", got)
	}
}

func TestExtractPreferredCredential_BodyAnthropicShape(t *testing.T) {
	adminKey := "ops-admin-token"
	// Anthropic metadata carries user_id + preferred_credential together.
	body := []byte(`{"model":"MiniMax-M3","max_tokens":16,"metadata":{"user_id":"u1","preferred_credential":"42"},"messages":[{"role":"user","content":"hi"}]}`)
	got := ExtractPreferredCredential("", body, adminKey, adminKey)
	if got != "42" {
		t.Errorf("anthropic-shaped body override: expected \"42\", got %q", got)
	}
}

func TestExtractPreferredCredential_HeaderPrecedenceOverBody(t *testing.T) {
	adminKey := "ops-admin-token"
	body := []byte(`{"metadata":{"preferred_credential":"36"}}`)
	// Header wins over body.
	got := ExtractPreferredCredential("42", body, adminKey, adminKey)
	if got != "42" {
		t.Errorf("header precedence: expected \"42\", got %q", got)
	}
}

func TestExtractPreferredCredential_NoOverride(t *testing.T) {
	adminKey := "ops-admin-token"
	// Neither header nor body carries the override — default routing.
	body := []byte(`{"model":"minimax-m3","messages":[]}`)
	got := ExtractPreferredCredential("", body, adminKey, adminKey)
	if got != "" {
		t.Errorf("no override: expected empty, got %q", got)
	}
}

func TestExtractPreferredCredential_NonNumericRejected(t *testing.T) {
	adminKey := "ops-admin-token"
	// Non-numeric id is rejected — routing's sticky lookup is by credential_id.
	got := ExtractPreferredCredential("not-a-number", nil, adminKey, adminKey)
	if got != "" {
		t.Errorf("non-numeric id: expected empty, got %q", got)
	}
}

func TestExtractPreferredCredential_NoBody(t *testing.T) {
	adminKey := "ops-admin-token"
	got := ExtractPreferredCredential("", nil, adminKey, adminKey)
	if got != "" {
		t.Errorf("empty body + empty header: expected empty, got %q", got)
	}
}
