package attachments

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// sanitizeKey validates a storage key and rejects attempts to escape the
// remote directory. Shared by all remote backends (Cloudreve, OSS, S3) so
// the safety contract is uniform across the storage matrix.
//
// Rejects:
//   - empty input
//   - path that resolves to root or current dir after Clean
//   - any traversal sequence (..) anywhere in the path
//
// Returns the cleaned path with leading slashes trimmed.
func sanitizeKey(key string) (string, error) {
	if key == "" {
		return "", errors.New("storage: empty key")
	}
	cleaned := strings.ReplaceAll(key, "\\", "/")
	cleaned = path.Clean(cleaned)
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("storage: key %q resolves to root", key)
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("storage: key %q escapes storage root", key)
	}
	return strings.TrimLeft(cleaned, "/"), nil
}
