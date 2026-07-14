//go:build storage_oss

package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/rs/zerolog/log"
)

// OSSStorageBackend implements StorageBackend against 阿里云对象存储 OSS.
//
// Configuration is sourced from the OSSConfig type defined alongside the
// StorageBackend interface in storage_backend.go (no duplicate declarations).
// Reference: aliyun-oss-go-sdk/oss (already in go.mod).
//
// Mapping to canonical StorageBackend methods:
//
//	Save / SaveReader → bucket.PutObject
//	Get / GetReader   → bucket.GetObject
//	Delete            → bucket.DeleteObject
//	Exists            → bucket.IsObjectExist
//	List              → bucket.ListObjects (paged)
//	GetMetadata       → bucket.GetObjectMeta
//
// Gated behind the storage_oss build tag so the default binary keeps no
// aliyun SDK linkage. Enable with:
//
//	go build -tags storage_oss ./cmd/gateway
type OSSStorageBackend struct {
	client     *oss.Client
	bucketName string
	prefix     string // object-key prefix inside the bucket
}

// NewOSSStorageBackend constructs an OSS backend against the provided bucket.
//
// Validation enforced here:
//   - Endpoint, AccessKeyID, AccessKeySecret, BucketName must all be set.
//   - Bucket must be reachable (GetBucketInfo).
//
// The returned backend is concurrency-safe — the OSS SDK's *oss.Client holds
// pooled HTTP connections internally.
func NewOSSStorageBackend(config *OSSConfig) (*OSSStorageBackend, error) {
	if config == nil {
		return nil, errors.New("oss storage: config is nil")
	}
	if config.Endpoint == "" {
		return nil, errors.New("oss storage: Endpoint is required")
	}
	if config.AccessKeyID == "" || config.AccessKeySecret == "" {
		return nil, errors.New("oss storage: AccessKeyID and AccessKeySecret are required")
	}
	if config.BucketName == "" {
		return nil, errors.New("oss storage: BucketName is required")
	}

	client, err := oss.New(config.Endpoint, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("oss storage: create client: %w", err)
	}

	// Validate bucket reachability by issuing a lightweight GetBucketACL.
	// This catches auth/permission issues without exposing a separate
	// health-check call later.
	if _, err = client.GetBucketACL(config.BucketName); err != nil {
		return nil, fmt.Errorf("oss storage: GetBucketACL (check creds and bucket): %w", err)
	}

	return &OSSStorageBackend{
		client:     client,
		bucketName: config.BucketName,
		prefix:     strings.Trim(config.BasePath, "/"),
	}, nil
}

// keyFor merges the storage backend prefix with the relative key. Mirrors
// LocalStorageBackend.getFilePath semantics (no traversal, forward slashes).
func (o *OSSStorageBackend) keyFor(storageKey string) (string, error) {
	cleaned, err := sanitizeKey(storageKey)
	if err != nil {
		return "", err
	}
	if o.prefix == "" {
		return cleaned, nil
	}
	return o.prefix + "/" + cleaned, nil
}

// bucket lazily resolves the *oss.Bucket. OSS SDK's Bucket() is cheap but not
// entirely free; we cache nothing because the SDK already pools connections.
func (o *OSSStorageBackend) bucket() (*oss.Bucket, error) {
	return o.client.Bucket(o.bucketName)
}

// Save uploads data via OSS PutObject. Streaming is delegated to the SDK
// (it reads from bytes.Reader internally). Atomic on the storage side — PUT
// overwrites in place, so this matches the WebDAV PUT path used by the
// Cloudreve adapter.
func (o *OSSStorageBackend) Save(ctx context.Context, key string, data []byte) error {
	ossKey, err := o.keyFor(key)
	if err != nil {
		return err
	}
	bucket, err := o.bucket()
	if err != nil {
		return fmt.Errorf("oss storage: get bucket: %w", err)
	}

	if err := bucket.PutObject(ossKey, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("oss storage: PutObject %q: %w", ossKey, err)
	}
	log.Debug().Str("key", ossKey).Int("size", len(data)).Msg("oss storage: saved")
	return nil
}

// SaveReader streams reader into OSS via the SDK's io.Reader overload.
// Caller is expected to provide the size up-front for proper Content-Length;
// the OSS SDK supports chunked transfer as a fallback when size is unknown,
// but we mirror the canonical contract and require it.
func (o *OSSStorageBackend) SaveReader(ctx context.Context, key string, reader io.Reader, size int64) error {
	if size < 0 {
		return errors.New("oss storage: SaveReader requires non-negative size")
	}
	ossKey, err := o.keyFor(key)
	if err != nil {
		return err
	}
	bucket, err := o.bucket()
	if err != nil {
		return fmt.Errorf("oss storage: get bucket: %w", err)
	}

	if err := bucket.PutObject(ossKey, reader); err != nil {
		return fmt.Errorf("oss storage: PutObjectReader %q: %w", ossKey, err)
	}
	return nil
}

// Get reads the full object into memory. For large files prefer GetReader.
func (o *OSSStorageBackend) Get(ctx context.Context, key string) ([]byte, error) {
	r, err := o.GetReader(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// GetReader opens a streaming GET. A NoSuchKey surface maps to a
// "file not found" error matching the LocalStorageBackend phrasing.
func (o *OSSStorageBackend) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	ossKey, err := o.keyFor(key)
	if err != nil {
		return nil, err
	}
	bucket, err := o.bucket()
	if err != nil {
		return nil, fmt.Errorf("oss storage: get bucket: %w", err)
	}

	reader, err := bucket.GetObject(ossKey)
	if err != nil {
		if isOSSNotFound(err) {
			return nil, fmt.Errorf("oss storage: file not found: %s", key)
		}
		return nil, fmt.Errorf("oss storage: GetObject %q: %w", ossKey, err)
	}
	return reader, nil
}

// Delete removes the object. OSS silently succeeds on missing objects, so we
// match the canonical contract by also returning nil in that case.
func (o *OSSStorageBackend) Delete(ctx context.Context, key string) error {
	ossKey, err := o.keyFor(key)
	if err != nil {
		return err
	}
	bucket, err := o.bucket()
	if err != nil {
		return fmt.Errorf("oss storage: get bucket: %w", err)
	}
	if err := bucket.DeleteObject(ossKey); err != nil {
		return fmt.Errorf("oss storage: DeleteObject %q: %w", ossKey, err)
	}
	return nil
}

// Exists uses OSS IsObjectExist — cheaper than a HEAD with full body.
func (o *OSSStorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	ossKey, err := o.keyFor(key)
	if err != nil {
		return false, err
	}
	bucket, err := o.bucket()
	if err != nil {
		return false, fmt.Errorf("oss storage: get bucket: %w", err)
	}
	exists, err := bucket.IsObjectExist(ossKey)
	if err != nil {
		return false, fmt.Errorf("oss storage: IsObjectExist %q: %w", ossKey, err)
	}
	return exists, nil
}

// GetMetadata fetches size / last-modified via OSS GetObjectMeta (HEAD-equivalent).
func (o *OSSStorageBackend) GetMetadata(ctx context.Context, key string) (*FileMetadata, error) {
	ossKey, err := o.keyFor(key)
	if err != nil {
		return nil, err
	}
	bucket, err := o.bucket()
	if err != nil {
		return nil, fmt.Errorf("oss storage: get bucket: %w", err)
	}

	meta, err := bucket.GetObjectMeta(ossKey)
	if err != nil {
		if isOSSNotFound(err) {
			return nil, fmt.Errorf("oss storage: file not found: %s", key)
		}
		return nil, fmt.Errorf("oss storage: GetObjectMeta %q: %w", ossKey, err)
	}

	var size int64
	if cl := meta.Get("Content-Length"); cl != "" {
		_, _ = fmt.Sscanf(cl, "%d", &size)
	}
	var modTime time.Time
	if lm := meta.Get("Last-Modified"); lm != "" {
		if t, perr := time.Parse(time.RFC1123, lm); perr == nil {
			modTime = t
		}
	}
	etag := strings.Trim(meta.Get("ETag"), `"`)

	return &FileMetadata{
		Key:          key,
		Size:         size,
		LastModified: modTime,
		ContentType:  meta.Get("Content-Type"),
		ETag:         etag,
	}, nil
}

// List paginates bucket.ListObjects under the requested prefix. Returns storage
// keys (i.e. the keys with the OSS prefix stripped) so the rest of the
// attachments package sees backend-agnostic paths.
func (o *OSSStorageBackend) List(ctx context.Context, prefix string) ([]string, error) {
	cleanedPrefix := prefix
	if cleanedPrefix != "" {
		c, err := sanitizeKey(cleanedPrefix)
		if err != nil {
			return nil, err
		}
		cleanedPrefix = c
	}

	// Compose OSS-side prefix from backend prefix + user-supplied prefix.
	ossPrefix := o.prefix
	if cleanedPrefix != "" {
		if ossPrefix != "" {
			ossPrefix = ossPrefix + "/" + cleanedPrefix
		} else {
			ossPrefix = cleanedPrefix
		}
	}

	bucket, err := o.bucket()
	if err != nil {
		return nil, fmt.Errorf("oss storage: get bucket: %w", err)
	}

	var keys []string
	marker := ""
	for {
		result, err := bucket.ListObjects(
			oss.Prefix(ossPrefix),
			oss.Marker(marker),
			oss.MaxKeys(1000),
		)
		if err != nil {
			return nil, fmt.Errorf("oss storage: ListObjects: %w", err)
		}
		for _, obj := range result.Objects {
			keys = append(keys, stripOSSPrefix(obj.Key, o.prefix))
		}
		if !result.IsTruncated {
			break
		}
		marker = result.NextMarker
	}
	return keys, nil
}

// GetBackendType returns the canonical name used by StorageConfig.Type.
func (o *OSSStorageBackend) GetBackendType() string {
	return "oss"
}

// HealthCheck calls GetBucketACL on the configured bucket — exercises both
// credential validity and network reachability. Mirrors the pattern used by
// the Cloudreve adapter's PROPFIND Depth:0 probe.
//
// S3StorageBackend uses HeadBucket (more appropriate for AWS); kept distinct
// because SDKs differ.
func (o *OSSStorageBackend) HealthCheck(ctx context.Context) error {
	if _, err := o.client.GetBucketACL(o.bucketName); err != nil {
		return fmt.Errorf("oss storage: health check failed: %w", err)
	}
	return nil
}

// stripOSSPrefix removes the configured backend prefix from an OSS object key
// to yield a storage key matching the canonical StorageBackend contract.
//
//	obj.Key = "attachments/2026/07/a1/b2/abc.png"
//	prefix  = "attachments"
//	→ "2026/07/a1/b2/abc.png"
func stripOSSPrefix(objKey, prefix string) string {
	if prefix == "" {
		return objKey
	}
	p := prefix + "/"
	if strings.HasPrefix(objKey, p) {
		return strings.TrimPrefix(objKey, p)
	}
	return objKey
}

// isOSSNotFound inspects any returned error and reports whether it represents
// a missing object. OSS uses ServiceError with StatusCode 404 for missing
// objects (and 203 in some legacy paths per SDK source).
//
// OSS returns ServiceError as a value type with a value-receiver Error()
// method. errors.As walks the wrap chain looking for either *oss.ServiceError
// or oss.ServiceError values; we try both.
func isOSSNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ptrTarget *oss.ServiceError
	if errors.As(err, &ptrTarget) && ptrTarget != nil {
		return ptrTarget.StatusCode == 404 || ptrTarget.StatusCode == 203
	}
	var valTarget oss.ServiceError
	if errors.As(err, &valTarget) {
		return valTarget.StatusCode == 404 || valTarget.StatusCode == 203
	}
	return false
}

// Compile-time check: OSSStorageBackend implements StorageBackend. Catches
// signature drift early at `go build` time instead of at the first request.
var _ StorageBackend = (*OSSStorageBackend)(nil)
