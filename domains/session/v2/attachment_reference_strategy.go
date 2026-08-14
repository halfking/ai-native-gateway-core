package v2

import (
	"strings"
	"time"
)

// AttachmentReferenceStrategy determines how to reference attachments when serializing to different providers
//
// Different providers have different capabilities for handling attachments:
// - Some support only data URIs (base64)
// - Some support public HTTPS URLs
// - Some have their own Files API (e.g., Gemini, Anthropic)
// - Some have size/format limitations

// AttachmentReferenceMode specifies how to reference an attachment
type AttachmentReferenceMode string

const (
	// RefModeDataURI uses base64-encoded data: URI (inline)
	RefModeDataURI AttachmentReferenceMode = "data"

	// RefModeGatewayURL uses gateway-hosted URL (files.kxpms.cn)
	RefModeGatewayURL AttachmentReferenceMode = "gateway_url"

	// RefModeProviderFile uses provider-specific file reference (e.g., Gemini file_uri)
	RefModeProviderFile AttachmentReferenceMode = "provider_file"

	// RefModePublicURL uses a public HTTPS URL
	RefModePublicURL AttachmentReferenceMode = "public_url"
)

// ProviderAttachmentCapability describes a provider's attachment handling capabilities
type ProviderAttachmentCapability struct {
	SupportsDataURI    bool
	SupportsHTTPSURL   bool
	SupportsFilesAPI   bool
	MaxInlineBytes     int64 // Max size for inline/data URI
	MaxURLBytes        int64 // Max size for URL reference
	PreferredMode      AttachmentReferenceMode
	SupportedMIMETypes []string // Empty means all
}

// GetProviderCapability returns attachment capabilities for a provider.
// Provider identifiers are normalized (trimmed, lowercased) since callers
// derive them from different sources (model registry, vendor config).
func GetProviderCapability(provider string) ProviderAttachmentCapability {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: true,             // OpenAI Files API
			MaxInlineBytes:   20 * 1024 * 1024, // 20MB for images
			MaxURLBytes:      20 * 1024 * 1024,
			PreferredMode:    RefModeGatewayURL, // URL preferred to reduce request size
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/gif", "image/webp",
				"audio/wav", "audio/mp3", "audio/mpeg",
			},
		}

	case "anthropic":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: true,              // Anthropic Files API (beta)
			MaxInlineBytes:   5 * 1024 * 1024,   // 5MB limit for base64
			MaxURLBytes:      100 * 1024 * 1024, // 100MB for documents via URL
			PreferredMode:    RefModeDataURI,    // base64 for smaller files, URL for larger
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/gif", "image/webp",
				"application/pdf", "text/plain", "text/html",
			},
		}

	case "gemini", "google":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: false, // Gemini doesn't support arbitrary URLs
			SupportsFilesAPI: true,  // Gemini Files API (recommended)
			MaxInlineBytes:   20 * 1024 * 1024,
			PreferredMode:    RefModeProviderFile, // Files API preferred
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/webp",
				"audio/wav", "audio/mp3", "audio/aac",
				"video/mp4", "video/mpeg", "video/mov",
				"application/pdf",
			},
		}

	case "deepseek":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: false,
			MaxInlineBytes:   10 * 1024 * 1024,
			PreferredMode:    RefModeDataURI,
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/webp",
			},
		}

	case "glm", "zhipu":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: false,
			MaxInlineBytes:   10 * 1024 * 1024,
			PreferredMode:    RefModeGatewayURL,
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg",
			},
		}

	case "minimax":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: false,
			MaxInlineBytes:   10 * 1024 * 1024,
			PreferredMode:    RefModeDataURI,
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/webp",
			},
		}

	case "qwen", "dashscope":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: false,
			MaxInlineBytes:   10 * 1024 * 1024,
			PreferredMode:    RefModeGatewayURL,
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/webp",
				"audio/wav", "audio/mp3",
			},
		}

	case "ollama":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: false, // Local models, no URL support
			SupportsFilesAPI: false,
			MaxInlineBytes:   50 * 1024 * 1024, // Local, more flexible
			PreferredMode:    RefModeDataURI,
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/webp",
			},
		}

	case "doubao", "volcengine":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: false,
			MaxInlineBytes:   10 * 1024 * 1024,
			PreferredMode:    RefModeGatewayURL,
			SupportedMIMETypes: []string{
				"image/png", "image/jpeg", "image/webp",
			},
		}

	default:
		// Conservative defaults for unknown providers
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: false,
			SupportsFilesAPI: false,
			MaxInlineBytes:   5 * 1024 * 1024,
			PreferredMode:    RefModeDataURI,
		}
	}
}

// defaultGatewayURLBase matches the default PublicURL in
// domains/attachments/config.go (LoadConfigFromEnv).
const defaultGatewayURLBase = "/api/attachments"

// gatewayURLBase is the public base for gateway-hosted attachment URLs.
// Wired once at startup via SetGatewayURLBase from attachments.Config.PublicURL;
// kept as an injectable value so this package stays decoupled from
// domains/attachments (see ir_attachment_adapter.go's mirror-struct note).
var gatewayURLBase = defaultGatewayURLBase

// SetGatewayURLBase sets the public base URL used by GatewayURL.
// Empty resets to the default. Startup wiring should pass
// attachments.Config.PublicURL (env LLM_GATEWAY_ATTACHMENT_PUBLIC_URL).
func SetGatewayURLBase(base string) {
	base = strings.TrimSpace(base)
	if base == "" {
		gatewayURLBase = defaultGatewayURLBase
		return
	}
	gatewayURLBase = strings.TrimRight(base, "/")
}

// GatewayURL builds the gateway-hosted URL for an attachment object key.
func GatewayURL(objectKey string) string {
	objectKey = strings.TrimLeft(objectKey, "/")
	if objectKey == "" {
		return gatewayURLBase
	}
	return gatewayURLBase + "/" + objectKey
}

// SelectReferenceMode determines the best way to reference an attachment for a target provider
//
// Decision factors:
// 1. Does the provider have a file reference we can use? (preferred)
// 2. Is the file too large for inline data URI?
// 3. Does the provider support HTTPS URLs?
// 4. Is the file replayable (not expired)?
func SelectReferenceMode(targetProvider string, attachment *AttachmentRef) AttachmentReferenceMode {
	if attachment == nil {
		return RefModeDataURI
	}
	capability := GetProviderCapability(targetProvider)
	now := time.Now()
	usableReplay := attachment.Replayable && (attachment.ExpiresAt.IsZero() || attachment.ExpiresAt.After(now))

	// If we have a provider-specific file ID and the target supports it, use it.
	if attachment.ProviderFileID != "" && capability.SupportsFilesAPI && usableReplay {
		return RefModeProviderFile
	}

	// If file is too large for inline, use a replayable URL when possible.
	if attachment.SizeBytes > capability.MaxInlineBytes {
		if capability.SupportsHTTPSURL && usableReplay {
			return RefModeGatewayURL
		}
		return RefModeDataURI
	}

	switch capability.PreferredMode {
	case RefModeProviderFile:
		if capability.SupportsFilesAPI && attachment.ProviderFileID != "" && usableReplay {
			return RefModeProviderFile
		}
		fallthrough
	case RefModeGatewayURL:
		if capability.SupportsHTTPSURL && usableReplay {
			return RefModeGatewayURL
		}
		fallthrough
	case RefModeDataURI:
		return RefModeDataURI
	default:
		return RefModeDataURI
	}
}

// ValidateAttachmentForProvider checks if an attachment is compatible with a provider
func ValidateAttachmentForProvider(provider string, attachment *AttachmentRef) error {
	if attachment == nil {
		return &AttachmentValidationError{Provider: provider, Reason: "attachment is nil"}
	}
	capability := GetProviderCapability(provider)

	// Check MIME type support
	if len(capability.SupportedMIMETypes) > 0 {
		supported := false
		for _, mimeType := range capability.SupportedMIMETypes {
			if attachment.MIMEType == mimeType {
				supported = true
				break
			}
		}
		if !supported {
			return &AttachmentValidationError{
				Provider:   provider,
				Attachment: attachment.ObjectKey,
				Reason:     "unsupported MIME type: " + attachment.MIMEType,
			}
		}
	}

	// Check size limits
	mode := SelectReferenceMode(provider, attachment)
	var maxSize int64

	switch mode {
	case RefModeDataURI:
		maxSize = capability.MaxInlineBytes
	case RefModeGatewayURL, RefModeProviderFile:
		maxSize = capability.MaxURLBytes
	}

	if maxSize > 0 && attachment.SizeBytes > maxSize {
		return &AttachmentValidationError{
			Provider:   provider,
			Attachment: attachment.ObjectKey,
			Reason:     "file too large for provider",
		}
	}

	return nil
}

// AttachmentValidationError represents an attachment validation failure
type AttachmentValidationError struct {
	Provider   string
	Attachment string
	Reason     string
}

func (e *AttachmentValidationError) Error() string {
	return "attachment validation failed for provider " + e.Provider +
		" (" + e.Attachment + "): " + e.Reason
}
