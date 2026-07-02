//go:build storage_oss

package attachments

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/rs/zerolog/log"
)

// OSSStorageBackend implements StorageBackend using Aliyun OSS
type OSSStorageBackend struct {
	client     *oss.Client
	bucketName string
	prefix     string
}

// OSSConfig holds configuration for OSS storage backend
type OSSConfig struct {
	Endpoint        string
	AccessKeyID     string
	AccessKeySecret string
	BucketName      string
	Prefix          string // Optional prefix for all keys
}

// NewOSSStorageBackend creates a new Aliyun OSS storage backend
func NewOSSStorageBackend(config OSSConfig) (*OSSStorageBackend, error) {
	if config.Endpoint == "" {
		return nil, fmt.Errorf("OSS endpoint cannot be empty")
	}
	if config.AccessKeyID == "" {
		return nil, fmt.Errorf("OSS access key ID cannot be empty")
	}
	if config.AccessKeySecret == "" {
		return nil, fmt.Errorf("OSS access key secret cannot be empty")
	}
	if config.BucketName == "" {
		return nil, fmt.Errorf("OSS bucket name cannot be empty")
	}

	// Create OSS client
	client, err := oss.New(config.Endpoint, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create OSS client: %w", err)
	}

	// Verify bucket exists
	bucket, err := client.Bucket(config.BucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket: %w", err)
	}

	// Test bucket access
	_, err = bucket.GetBucketInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to access bucket (check permissions): %w", err)
	}

	return &OSSStorageBackend{
		client:     client,
		bucketName: config.BucketName,
		prefix:     strings.TrimSuffix(config.Prefix, "/"),
	}, nil
}

// Save stores a file and returns its storage key
func (s *OSSStorageBackend) Save(reader io.Reader, metadata StorageMetadata) (string, error) {
	// Calculate hash while reading
	hash := sha256.New()
	teeReader := io.TeeReader(reader, hash)

	// Buffer the content to allow retry and get size
	content, err := io.ReadAll(teeReader)
	if err != nil {
		return "", fmt.Errorf("failed to read content: %w", err)
	}

	if len(content) == 0 {
		return "", fmt.Errorf("empty file not allowed")
	}

	// Generate storage key based on hash
	hashStr := hex.EncodeToString(hash.Sum(nil))
	storageKey := s.generateStorageKey(hashStr, metadata.Filename)
	ossKey := s.getOSSKey(storageKey)

	bucket, err := s.client.Bucket(s.bucketName)
	if err != nil {
		return "", fmt.Errorf("failed to get bucket: %w", err)
	}

	// Check if object already exists (deduplication)
	exists, err := bucket.IsObjectExist(ossKey)
	if err != nil {
		log.Warn().Err(err).Str("oss_key", ossKey).Msg("Failed to check object existence, proceeding with upload")
	} else if exists {
		log.Debug().
			Str("storage_key", storageKey).
			Str("filename", metadata.Filename).
			Msg("Object already exists in OSS, reusing existing object")
		return storageKey, nil
	}

	// Prepare options
	options := []oss.Option{
		oss.ContentType(metadata.ContentType),
		oss.ContentDisposition(fmt.Sprintf("attachment; filename=\"%s\"", metadata.Filename)),
	}

	// Upload to OSS
	err = bucket.PutObject(ossKey, strings.NewReader(string(content)), options...)
	if err != nil {
		return "", fmt.Errorf("failed to upload to OSS: %w", err)
	}

	log.Info().
		Str("storage_key", storageKey).
		Str("oss_key", ossKey).
		Str("filename", metadata.Filename).
		Int("size", len(content)).
		Str("content_type", metadata.ContentType).
		Msg("File uploaded to OSS successfully")

	return storageKey, nil
}

// Get retrieves a file by its storage key
func (s *OSSStorageBackend) Get(storageKey string) (io.ReadCloser, error) {
	ossKey := s.getOSSKey(storageKey)

	bucket, err := s.client.Bucket(s.bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket: %w", err)
	}

	reader, err := bucket.GetObject(ossKey)
	if err != nil {
		if ossErr, ok := err.(oss.ServiceError); ok && ossErr.Code == "NoSuchKey" {
			return nil, fmt.Errorf("file not found: %s", storageKey)
		}
		return nil, fmt.Errorf("failed to get object from OSS: %w", err)
	}

	return reader, nil
}

// Delete removes a file by its storage key
func (s *OSSStorageBackend) Delete(storageKey string) error {
	ossKey := s.getOSSKey(storageKey)

	bucket, err := s.client.Bucket(s.bucketName)
	if err != nil {
		return fmt.Errorf("failed to get bucket: %w", err)
	}

	err = bucket.DeleteObject(ossKey)
	if err != nil {
		// OSS doesn't error on deleting non-existent objects
		return fmt.Errorf("failed to delete object from OSS: %w", err)
	}

	log.Info().
		Str("storage_key", storageKey).
		Str("oss_key", ossKey).
		Msg("Object deleted from OSS successfully")

	return nil
}

// Exists checks if a file exists by its storage key
func (s *OSSStorageBackend) Exists(storageKey string) (bool, error) {
	ossKey := s.getOSSKey(storageKey)

	bucket, err := s.client.Bucket(s.bucketName)
	if err != nil {
		return false, fmt.Errorf("failed to get bucket: %w", err)
	}

	exists, err := bucket.IsObjectExist(ossKey)
	if err != nil {
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	return exists, nil
}

// GetMetadata retrieves metadata for a file
func (s *OSSStorageBackend) GetMetadata(storageKey string) (*StorageMetadata, error) {
	ossKey := s.getOSSKey(storageKey)

	bucket, err := s.client.Bucket(s.bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket: %w", err)
	}

	meta, err := bucket.GetObjectMeta(ossKey)
	if err != nil {
		if ossErr, ok := err.(oss.ServiceError); ok && ossErr.Code == "NoSuchKey" {
			return nil, fmt.Errorf("file not found: %s", storageKey)
		}
		return nil, fmt.Errorf("failed to get object metadata: %w", err)
	}

	// Extract filename from storage key
	filename := s.extractFilename(storageKey)

	// Parse last modified time
	var createdAt time.Time
	if lastModified := meta.Get("Last-Modified"); lastModified != "" {
		createdAt, _ = time.Parse(time.RFC1123, lastModified)
	}

	// Parse content length
	var size int64
	if contentLength := meta.Get("Content-Length"); contentLength != "" {
		fmt.Sscanf(contentLength, "%d", &size)
	}

	return &StorageMetadata{
		Filename:    filename,
		Size:        size,
		ContentType: meta.Get("Content-Type"),
		CreatedAt:   createdAt,
	}, nil
}

// List lists all files with optional prefix filter
func (s *OSSStorageBackend) List(prefix string) ([]string, error) {
	bucket, err := s.client.Bucket(s.bucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket: %w", err)
	}

	// Construct OSS prefix
	ossPrefix := s.prefix
	if prefix != "" {
		if ossPrefix != "" {
			ossPrefix = ossPrefix + "/" + prefix
		} else {
			ossPrefix = prefix
		}
	}

	var keys []string
	marker := ""

	for {
		result, err := bucket.ListObjects(oss.Prefix(ossPrefix), oss.Marker(marker), oss.MaxKeys(1000))
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range result.Objects {
			// Convert OSS key back to storage key
			storageKey := s.ossKeyToStorageKey(obj.Key)
			if storageKey != "" {
				keys = append(keys, storageKey)
			}
		}

		if !result.IsTruncated {
			break
		}
		marker = result.NextMarker
	}

	return keys, nil
}

// GetURL returns a presigned URL for accessing the file
func (s *OSSStorageBackend) GetURL(storageKey string, expiry time.Duration) (string, error) {
	ossKey := s.getOSSKey(storageKey)

	bucket, err := s.client.Bucket(s.bucketName)
	if err != nil {
		return "", fmt.Errorf("failed to get bucket: %w", err)
	}

	// Generate presigned URL with expiry
	expirySeconds := int64(expiry.Seconds())
	if expirySeconds <= 0 {
		expirySeconds = 3600 // Default 1 hour
	}

	url, err := bucket.SignURL(ossKey, oss.HTTPGet, expirySeconds)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return url, nil
}

// generateStorageKey creates a storage key from hash and filename
// Format: <prefix>/<hash>/<filename>
func (s *OSSStorageBackend) generateStorageKey(hash, filename string) string {
	// Use first 2 chars of hash as prefix for better distribution
	prefix := hash[:2]
	return fmt.Sprintf("%s/%s/%s", prefix, hash, filename)
}

// getOSSKey converts a storage key to an OSS object key
func (s *OSSStorageBackend) getOSSKey(storageKey string) string {
	if s.prefix != "" {
		return s.prefix + "/" + storageKey
	}
	return storageKey
}

// ossKeyToStorageKey converts an OSS object key back to storage key
func (s *OSSStorageBackend) ossKeyToStorageKey(ossKey string) string {
	if s.prefix != "" {
		return strings.TrimPrefix(ossKey, s.prefix+"/")
	}
	return ossKey
}

// extractFilename extracts the filename from a storage key
func (s *OSSStorageBackend) extractFilename(storageKey string) string {
	parts := strings.Split(storageKey, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return storageKey
}
