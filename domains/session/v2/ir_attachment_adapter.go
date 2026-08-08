package v2

import (
	"time"
)

// IRAttachmentAdapter bridges between IR layer and session v2 storage
//
// This adapter extracts multimodal content from IR structures and converts
// them to AttachmentRef for storage in session_bodies table.

// AttachmentMetadata represents attachment information from the attachment storage layer
// This mirrors the structure from domains/attachments but is defined here to avoid imports
type AttachmentMetadata struct {
	ObjectKey      string
	OriginalName   string
	MIMEType       string
	DeclaredMIME   string
	SniffedMIME    string
	SizeBytes      int64
	SHA256         string
	SourceProtocol string
	ProviderFileID string
	ExpiresAt      time.Time
	Replayable     bool
}

// ConvertToAttachmentRef converts AttachmentMetadata to session v2 AttachmentRef
func ConvertToAttachmentRef(meta AttachmentMetadata) AttachmentRef {
	return AttachmentRef{
		Name:           meta.OriginalName,
		ObjectKey:      meta.ObjectKey,
		MIMEType:       meta.MIMEType,
		SizeBytes:      meta.SizeBytes,
		SHA256:         meta.SHA256,
		SourceProtocol: meta.SourceProtocol,
		DeclaredMIME:   meta.DeclaredMIME,
		SniffedMIME:    meta.SniffedMIME,
		ProviderFileID: meta.ProviderFileID,
		ExpiresAt:      meta.ExpiresAt,
		Replayable:     meta.Replayable,
	}
}

// ConvertBatch converts a batch of AttachmentMetadata to AttachmentRef
func ConvertBatch(metas []AttachmentMetadata) []AttachmentRef {
	refs := make([]AttachmentRef, len(metas))
	for i, meta := range metas {
		refs[i] = ConvertToAttachmentRef(meta)
	}
	return refs
}

// ExtractMultimodalTypes extracts unique multimodal content types from attachments
//
// Returns a deduplicated list of types: ["image", "audio", "video", "document"]
func ExtractMultimodalTypes(attachments []AttachmentRef) []string {
	typeSet := make(map[string]bool)

	for _, att := range attachments {
		contentType := categorizeContentType(att.MIMEType)
		if contentType != "" {
			typeSet[contentType] = true
		}
	}

	// Convert set to sorted list
	types := make([]string, 0, len(typeSet))
	order := []string{"image", "audio", "video", "document"} // Stable order
	for _, t := range order {
		if typeSet[t] {
			types = append(types, t)
		}
	}

	return types
}

// categorizeContentType maps MIME type to multimodal category
func categorizeContentType(mimeType string) string {
	if len(mimeType) == 0 {
		return ""
	}

	// Check prefix
	if len(mimeType) >= 6 {
		switch mimeType[:6] {
		case "image/":
			return "image"
		case "audio/":
			return "audio"
		case "video/":
			return "video"
		}
	}

	// Check common document types
	switch mimeType {
	case "application/pdf":
		return "document"
	case "text/plain", "text/html", "text/markdown":
		return "document"
	case "application/msword", "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "document"
	case "application/vnd.ms-excel", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "document"
	case "application/json", "application/xml":
		return "document"
	}

	return ""
}

// CalculateAttachmentStats computes aggregate statistics for attachments
type AttachmentStats struct {
	Count      int
	TotalBytes int64
	Types      []string
}

// ComputeStats calculates statistics from attachment list
func ComputeStats(attachments []AttachmentRef) AttachmentStats {
	var totalBytes int64
	for _, att := range attachments {
		totalBytes += att.SizeBytes
	}

	return AttachmentStats{
		Count:      len(attachments),
		TotalBytes: totalBytes,
		Types:      ExtractMultimodalTypes(attachments),
	}
}
