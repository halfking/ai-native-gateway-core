package v2

import (
	"strings"
	"testing"
	"time"
)

// TestProviderCapabilityMatrix 钉住各 provider 的附件能力矩阵（MM-1a）。
// 该矩阵是 outbound 渲染选择 data/gateway_url/provider_file 引用模式的依据。
func TestProviderCapabilityMatrix(t *testing.T) {
	tests := []struct {
		provider       string
		wantMode       AttachmentReferenceMode
		wantDataURI    bool
		wantHTTPSURL   bool
		wantFilesAPI   bool
		wantMinInlines int64 // MaxInlineBytes 下限（字节）
	}{
		{"openai", RefModeGatewayURL, true, true, true, 20 << 20},
		{"anthropic", RefModeDataURI, true, true, true, 5 << 20},
		{"gemini", RefModeProviderFile, true, false, true, 20 << 20},
		{"google", RefModeProviderFile, true, false, true, 20 << 20},
		{"deepseek", RefModeDataURI, true, false, false, 10 << 20},
		{"glm", RefModeGatewayURL, true, true, false, 10 << 20},
		{"zhipu", RefModeGatewayURL, true, true, false, 10 << 20},
		{"minimax", RefModeDataURI, true, true, false, 10 << 20},
		{"qwen", RefModeGatewayURL, true, true, false, 10 << 20},
		{"dashscope", RefModeGatewayURL, true, true, false, 10 << 20},
		{"ollama", RefModeDataURI, true, false, false, 50 << 20},
		{"doubao", RefModeGatewayURL, true, true, false, 10 << 20},
		{"volcengine", RefModeGatewayURL, true, true, false, 10 << 20},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			capability := GetProviderCapability(tt.provider)
			if capability.PreferredMode != tt.wantMode {
				t.Errorf("PreferredMode = %q, want %q", capability.PreferredMode, tt.wantMode)
			}
			if capability.SupportsDataURI != tt.wantDataURI {
				t.Errorf("SupportsDataURI = %v, want %v", capability.SupportsDataURI, tt.wantDataURI)
			}
			if capability.SupportsHTTPSURL != tt.wantHTTPSURL {
				t.Errorf("SupportsHTTPSURL = %v, want %v", capability.SupportsHTTPSURL, tt.wantHTTPSURL)
			}
			if capability.SupportsFilesAPI != tt.wantFilesAPI {
				t.Errorf("SupportsFilesAPI = %v, want %v", capability.SupportsFilesAPI, tt.wantFilesAPI)
			}
			if capability.MaxInlineBytes < tt.wantMinInlines {
				t.Errorf("MaxInlineBytes = %d, want >= %d", capability.MaxInlineBytes, tt.wantMinInlines)
			}
		})
	}
}

// 未知 provider 必须拿到保守默认值：只允许 data URI、不支持 URL/Files API。
func TestProviderCapability_UnknownProviderIsConservative(t *testing.T) {
	capability := GetProviderCapability("some-unknown-vendor")
	if capability.PreferredMode != RefModeDataURI || capability.SupportsHTTPSURL || capability.SupportsFilesAPI {
		t.Errorf("unknown provider should be conservative, got %+v", capability)
	}
}

// provider 大小写/别名不敏感（路由侧 provider 标识来源不一）。
func TestProviderCapability_IsCaseInsensitive(t *testing.T) {
	canonical := GetProviderCapability("openai")
	for _, alias := range []string{"OpenAI", "OPENAI", " openai"} {
		got := GetProviderCapability(alias)
		if got.PreferredMode != canonical.PreferredMode || got.SupportsHTTPSURL != canonical.SupportsHTTPSURL {
			t.Errorf("GetProviderCapability(%q) mismatched canonical openai: %+v", alias, got)
		}
	}
}

func replayablePNG(size int64) *AttachmentRef {
	return &AttachmentRef{
		ObjectKey:  "2026/08/a1/b2/hash.png",
		MIMEType:   "image/png",
		SizeBytes:  size,
		SHA256:     "hash",
		Replayable: true,
	}
}

func TestSelectReferenceMode_DecisionMatrix(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		attachment *AttachmentRef
		want       AttachmentReferenceMode
	}{
		// Files API 优先：有 provider file ID 且可重放
		{"gemini file id replayable", "gemini", func() *AttachmentRef {
			a := replayablePNG(1024)
			a.ProviderFileID = "files/abc"
			return a
		}(), RefModeProviderFile},
		// Files API 不可重放 → 退回 provider 首选模式
		{"gemini file id not replayable", "gemini", func() *AttachmentRef {
			a := replayablePNG(1024)
			a.ProviderFileID = "files/abc"
			a.Replayable = false
			return a
		}(), RefModeDataURI},
		// openai 首选 gateway URL
		{"openai small image", "openai", replayablePNG(1024), RefModeGatewayURL},
		// URL 不可重放 → data URI 兜底
		{"openai not replayable", "openai", func() *AttachmentRef {
			a := replayablePNG(1024)
			a.Replayable = false
			return a
		}(), RefModeDataURI},
		// 过大不能内联：支持 URL 且可重放 → gateway URL
		{"anthropic large replayable", "anthropic", replayablePNG(50 << 20), RefModeGatewayURL},
		// 过大不能内联：不可重放 → 仍 data URI（保守）
		{"anthropic large not replayable", "anthropic", func() *AttachmentRef {
			a := replayablePNG(50 << 20)
			a.Replayable = false
			return a
		}(), RefModeDataURI},
		// 不支持 URL 的 provider 永远 data URI
		{"ollama any size", "ollama", replayablePNG(60 << 20), RefModeDataURI},
		// 已过期的 provider file 不可用
		{"gemini file expired", "gemini", func() *AttachmentRef {
			a := replayablePNG(1024)
			a.ProviderFileID = "files/abc"
			a.ExpiresAt = time.Now().Add(-time.Hour)
			return a
		}(), RefModeDataURI},
		{"nil attachment", "openai", nil, RefModeDataURI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectReferenceMode(tt.provider, tt.attachment)
			if got != tt.want {
				t.Errorf("SelectReferenceMode(%s) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestValidateAttachmentForProvider(t *testing.T) {
	t.Run("supported png for openai", func(t *testing.T) {
		if err := ValidateAttachmentForProvider("openai", replayablePNG(1024)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("unsupported mime for anthropic audio", func(t *testing.T) {
		a := replayablePNG(1024)
		a.MIMEType = "audio/ogg"
		err := ValidateAttachmentForProvider("anthropic", a)
		if err == nil || !strings.Contains(err.Error(), "unsupported MIME type") {
			t.Errorf("want unsupported MIME error, got %v", err)
		}
	})
	t.Run("oversize for ollama", func(t *testing.T) {
		err := ValidateAttachmentForProvider("ollama", replayablePNG(60<<20))
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("want too-large error, got %v", err)
		}
	})
	t.Run("nil attachment", func(t *testing.T) {
		if err := ValidateAttachmentForProvider("openai", nil); err == nil {
			t.Error("want error for nil attachment")
		}
	})
}

// TestGatewayURL verifies gateway-hosted URL construction for RefModeGatewayURL.
// The base is wired from attachments.Config.PublicURL (domains/attachments/config.go);
// default matches that config ("/api/attachments/").
func TestGatewayURL(t *testing.T) {
	t.Run("default base", func(t *testing.T) {
		resetGatewayURLBase()
		defer resetGatewayURLBase()
		got := GatewayURL("2026/08/a1/b2/hash.png")
		if got != "/api/attachments/2026/08/a1/b2/hash.png" {
			t.Errorf("GatewayURL = %q", got)
		}
	})
	t.Run("custom base from attachments config", func(t *testing.T) {
		resetGatewayURLBase()
		defer resetGatewayURLBase()
		SetGatewayURLBase("https://cdn.example.com/attachments/")
		got := GatewayURL("2026/08/a1/b2/hash.png")
		if got != "https://cdn.example.com/attachments/2026/08/a1/b2/hash.png" {
			t.Errorf("GatewayURL = %q", got)
		}
	})
	t.Run("empty key returns base", func(t *testing.T) {
		resetGatewayURLBase()
		defer resetGatewayURLBase()
		SetGatewayURLBase("https://cdn.example.com/attachments")
		if got := GatewayURL(""); got != "https://cdn.example.com/attachments" {
			t.Errorf("GatewayURL(\"\") = %q", got)
		}
	})
}

// resetGatewayURLBase restores the default gateway URL base.
func resetGatewayURLBase() {
	SetGatewayURLBase("")
}
