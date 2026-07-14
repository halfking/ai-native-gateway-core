package attachments

import (
	"path/filepath"
	"strings"
)

// detectContentType maps a file extension to a MIME type. Used by the S3
// backend to set Content-Type on PutObject so that previews / direct
// downloads get the right handler.
//
// Falls back to application/octet-stream so unknown extensions are still
// storable — the upstream caller can override ContentType via metadata if
// needed.
func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "application/octet-stream"
	}
	contentTypes := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
		".svg":  "image/svg+xml",
		".pdf":  "application/pdf",
		".txt":  "text/plain",
		".json": "application/json",
		".xml":  "application/xml",
		".zip":  "application/zip",
		".mp4":  "video/mp4",
		".mp3":  "audio/mpeg",
	}
	if ct, ok := contentTypes[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}
