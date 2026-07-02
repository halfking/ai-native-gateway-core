//go:build !storage_oss

package attachments

import "fmt"

// NewOSSStorageBackend creates a stub OSS backend (not available without storage_oss build tag)
func NewOSSStorageBackend(config *OSSConfig) (StorageBackend, error) {
	return nil, fmt.Errorf("OSS storage backend not available - build with -tags storage_oss")
}
