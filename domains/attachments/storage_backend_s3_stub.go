//go:build !storage_s3

package attachments

import "fmt"

// NewS3StorageBackend creates a stub S3 backend (not available without storage_s3 build tag)
func NewS3StorageBackend(config *S3Config) (StorageBackend, error) {
	return nil, fmt.Errorf("S3 storage backend not available - build with -tags storage_s3")
}
