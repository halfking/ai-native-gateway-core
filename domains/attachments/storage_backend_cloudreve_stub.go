//go:build !cloudreve_storage

package attachments

import (
	"errors"
	"time"
)

// CloudreveConfig configures a Cloudreve StorageBackend. The real
// implementation lives in storage_backend_cloudreve.go and is gated behind
// the `cloudreve_storage` build tag.
type CloudreveConfig struct {
	BaseURL    string
	Username   string
	Password   string
	RemotePath string
	Timeout    time.Duration
}

// NewCloudreveStorageBackend returns an error when the cloudreve_storage
// build tag is not set. Build with `-tags cloudreve_storage` to enable.
func NewCloudreveStorageBackend(_ CloudreveConfig) (StorageBackend, error) {
	return nil, errors.New("Cloudreve storage backend not available - build with -tags cloudreve_storage")
}
