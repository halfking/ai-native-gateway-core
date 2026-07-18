package v2

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
	SupportsDataURI     bool
	SupportsHTTPSURL    bool
	SupportsFilesAPI    bool
	MaxInlineBytes      int64  // Max size for inline/data URI
	MaxURLBytes         int64  // Max size for URL reference
	PreferredMode       AttachmentReferenceMode
	SupportedMIMETypes  []string // Empty means all
}

// GetProviderCapability returns attachment capabilities for a provider
func GetProviderCapability(provider string) ProviderAttachmentCapability {
	switch provider {
	case "openai":
		return ProviderAttachmentCapability{
			SupportsDataURI:  true,
			SupportsHTTPSURL: true,
			SupportsFilesAPI: true, // OpenAI Files API
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
			SupportsFilesAPI: true, // Anthropic Files API (beta)
			MaxInlineBytes:   5 * 1024 * 1024,  // 5MB limit for base64
			MaxURLBytes:      100 * 1024 * 1024, // 100MB for documents via URL
			PreferredMode:    RefModeDataURI, // base64 for smaller files, URL for larger
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

// SelectReferenceMode determines the best way to reference an attachment for a target provider
//
// Decision factors:
// 1. Does the provider have a file reference we can use? (preferred)
// 2. Is the file too large for inline data URI?
// 3. Does the provider support HTTPS URLs?
// 4. Is the file replayable (not expired)?
func SelectReferenceMode(targetProvider string, attachment *AttachmentRef) AttachmentReferenceMode {
	capability := GetProviderCapability(targetProvider)
	
	// If we have a provider-specific file ID and the target supports it, use it
	if attachment.ProviderFileID != "" && capability.SupportsFilesAPI {
		// Check if the file hasn't expired
		if attachment.Replayable && !attachment.ExpiresAt.IsZero() {
			return RefModeProviderFile
		}
	}
	
	// If file is too large for inline, must use URL
	if attachment.SizeBytes > capability.MaxInlineBytes {
		if capability.SupportsHTTPSURL && attachment.Replayable {
			return RefModeGatewayURL
		}
		// File too large and no URL support - this will fail
		// Caller should handle this case
		return RefModeDataURI // Return default, caller must validate
	}
	
	// Small files: prefer based on provider preference
	switch capability.PreferredMode {
	case RefModeProviderFile:
		if capability.SupportsFilesAPI && attachment.ProviderFileID != "" {
			return RefModeProviderFile
		}
		fallthrough // If Files API not available, try URL
		
	case RefModeGatewayURL:
		if capability.SupportsHTTPSURL && attachment.Replayable {
			return RefModeGatewayURL
		}
		fallthrough // If URL not supported, use data URI
		
	case RefModeDataURI:
		return RefModeDataURI
		
	default:
		return RefModeDataURI
	}
}

// ValidateAttachmentForProvider checks if an attachment is compatible with a provider
func ValidateAttachmentForProvider(provider string, attachment *AttachmentRef) error {
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
