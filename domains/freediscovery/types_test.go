package freediscovery

import (
	"testing"
)

func TestValidateCreate_MissingProviderCode(t *testing.T) {
	req := &CreateTemplateRequest{DisplayName: "x", BaseURL: "https://api.groq.com/openai/v1"}
	if msg := req.ValidateCreate(); msg != "provider_code is required" {
		t.Fatalf("got %q", msg)
	}
}

func TestValidateCreate_ProviderCodeCharset(t *testing.T) {
	req := &CreateTemplateRequest{
		ProviderCode: "Groq!", DisplayName: "x", BaseURL: "https://a.com",
	}
	if msg := req.ValidateCreate(); msg == "" {
		t.Fatal("uppercase/special chars provider_code must be rejected")
	}

	req.ProviderCode = "groq"
	if msg := req.ValidateCreate(); msg != "" {
		t.Fatalf("valid provider_code rejected: %q", msg)
	}
}

func TestValidateCreate_ProviderCodeTooLong(t *testing.T) {
	code := make([]byte, 65)
	for i := range code {
		code[i] = 'a'
	}
	req := &CreateTemplateRequest{ProviderCode: string(code), DisplayName: "x", BaseURL: "https://a.com"}
	if msg := req.ValidateCreate(); msg == "" {
		t.Fatal("65-char provider_code must be rejected")
	}
}

func TestValidateCreate_MissingBaseURL(t *testing.T) {
	req := &CreateTemplateRequest{ProviderCode: "groq", DisplayName: "x"}
	if msg := req.ValidateCreate(); msg != "base_url is required" {
		t.Fatalf("got %q", msg)
	}
}

func TestValidateCreate_BaseURLScheme(t *testing.T) {
	req := &CreateTemplateRequest{ProviderCode: "groq", DisplayName: "x", BaseURL: "ftp://api.groq.com"}
	if msg := req.ValidateCreate(); msg == "" {
		t.Fatal("ftp:// base_url must be rejected")
	}
}

func TestValidateCreate_DefaultsAPIType(t *testing.T) {
	req := &CreateTemplateRequest{ProviderCode: "groq", DisplayName: "x", BaseURL: "https://a.com"}
	if msg := req.ValidateCreate(); msg != "" {
		t.Fatalf("unexpected rejection: %q", msg)
	}
	if req.APIType != APITypeOpenAICompletions {
		t.Fatalf("empty api_type should default to openai-completions, got %q", req.APIType)
	}
}

func TestValidateCreate_InvalidAPIType(t *testing.T) {
	req := &CreateTemplateRequest{
		ProviderCode: "groq", DisplayName: "x", BaseURL: "https://a.com",
		APIType: "websocket-magic",
	}
	if msg := req.ValidateCreate(); msg == "" {
		t.Fatal("unknown api_type must be rejected")
	}
}

func TestValidateCreate_InvalidTosVerdict(t *testing.T) {
	req := &CreateTemplateRequest{
		ProviderCode: "groq", DisplayName: "x", BaseURL: "https://a.com",
		TosVerdict: "probably-fine",
	}
	if msg := req.ValidateCreate(); msg == "" {
		t.Fatal("invalid tos_verdict must be rejected")
	}
}

func TestProviderTemplate_HasCredential(t *testing.T) {
	var nilT *ProviderTemplate
	if nilT.HasCredential() {
		t.Fatal("nil template must not report credentials")
	}
	tpl := &ProviderTemplate{}
	if tpl.HasCredential() {
		t.Fatal("keyless template must not report credentials")
	}
	tpl.APIKeyEnv = "$GROQ_API_KEY"
	if !tpl.HasCredential() {
		t.Fatal("env-referenced key must count as credential")
	}
	tpl2 := &ProviderTemplate{APIKeyEncrypted: []byte("ciphertext")}
	if !tpl2.HasCredential() {
		t.Fatal("encrypted key must count as credential")
	}
}
