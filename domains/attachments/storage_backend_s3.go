//go:build storage_s3

package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3StorageBackend implements StorageBackend against AWS S3 or any
// S3-compatible service (MinIO, Ceph, etc.).
//
// Configuration is sourced from the S3Config type defined alongside the
// StorageBackend interface in storage_backend.go (no duplicate declarations).
//
// Reference: aws-sdk-go-v2 (already in go.mod).
//
// Mapping to canonical StorageBackend methods:
//
//	Save / SaveReader → s3.PutObject
//	Get / GetReader   → s3.GetObject
//	Delete            → s3.DeleteObject
//	Exists            → s3.HeadObject (404 = false)
//	List              → s3.ListObjectsV2 paginator
//	GetMetadata       → s3.HeadObject (Content-Length / LastModified)
//
// Gated behind the storage_s3 build tag so the default binary keeps no
// aws-sdk linkage. Enable with:
//
//	go build -tags storage_s3 ./cmd/gateway
type S3StorageBackend struct {
	client   *awss3.Client
	bucket   string
	prefix   string
	endpoint string
}

// NewS3StorageBackend constructs an S3 backend.
//
// Validation enforced here:
//   - BucketName must be set.
//   - Region defaults to "us-east-1" when blank (AWS SDK requires it).
//   - For S3-compatible services (MinIO), set Endpoint + UsePathStyle=true.
//
// When credentials are absent the SDK falls back to its default provider
// chain (env vars → shared config → IRSA for EKS). Useful for in-cluster
// deployments where static creds should not be baked into the image.
func NewS3StorageBackend(config *S3Config) (*S3StorageBackend, error) {
	if config == nil {
		return nil, errors.New("s3 storage: config is nil")
	}
	if config.BucketName == "" {
		return nil, errors.New("s3 storage: BucketName is required")
	}
	region := config.Region
	if region == "" {
		region = "us-east-1"
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if config.AccessKeyID != "" && config.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				config.AccessKeyID,
				config.SecretAccessKey,
				"",
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("s3 storage: load aws config: %w", err)
	}

	clientOpts := []func(*awss3.Options){}
	if config.Endpoint != "" {
		// Custom endpoint for MinIO/Cloudflare R2/Ceph RGW.
		clientOpts = append(clientOpts, func(o *awss3.Options) {
			o.BaseEndpoint = aws.String(config.Endpoint)
			o.UsePathStyle = config.UsePathStyle
		})
	}

	client := awss3.NewFromConfig(awsCfg, clientOpts...)

	return &S3StorageBackend{
		client:   client,
		bucket:   config.BucketName,
		prefix:   strings.Trim(config.BasePath, "/"),
		endpoint: config.Endpoint,
	}, nil
}

// keyFor merges prefix with the storage key. Mirrors Cloudreve's adapter.
func (s *S3StorageBackend) keyFor(storageKey string) (string, error) {
	cleaned, err := sanitizeKey(storageKey)
	if err != nil {
		return "", err
	}
	if s.prefix == "" {
		return cleaned, nil
	}
	return s.prefix + "/" + cleaned, nil
}

// stripPrefix is the inverse of keyFor — used by List.
func (s *S3StorageBackend) stripPrefix(objectKey string) string {
	if s.prefix == "" {
		return objectKey
	}
	p := s.prefix + "/"
	return strings.TrimPrefix(objectKey, p)
}

// Save uploads data. PUT is idempotent, so retries are safe.
func (s *S3StorageBackend) Save(ctx context.Context, key string, data []byte) error {
	objectKey, err := s.keyFor(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(detectContentType(objectKey)),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: PutObject %q: %w", objectKey, err)
	}
	return nil
}

// SaveReader streams the reader into S3 via the SDK's io.Reader overload.
//
// S3 streaming caveat: aws-sdk-go-v2 requires a seekable body or a precomputed
// checksum to avoid double-buffering inside the SDK. We materialize into a
// bytes.Reader so the SDK can compute its checksum cheaply. For callers that
// need true streaming across the wire they should use multipart upload
// directly — outside the scope of this single-shot backend.
//
// Memory cost: O(body size). Acceptable for the canonical Attachment use
// case (≤20MB per the LocalStorageBackend contract).
func (s *S3StorageBackend) SaveReader(ctx context.Context, key string, reader io.Reader, size int64) error {
	if size < 0 {
		return errors.New("s3 storage: SaveReader requires non-negative size")
	}
	objectKey, err := s.keyFor(key)
	if err != nil {
		return err
	}

	buf, err := io.ReadAll(io.LimitReader(reader, size))
	if err != nil {
		return fmt.Errorf("s3 storage: read body for %q: %w", objectKey, err)
	}
	body := bytes.NewReader(buf)
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		Body:          body,
		ContentLength: aws.Int64(int64(len(buf))),
		ContentType:   aws.String(detectContentType(objectKey)),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: PutObjectReader %q: %w", objectKey, err)
	}
	return nil
}

// Get returns the full object body. For large files prefer GetReader.
func (s *S3StorageBackend) Get(ctx context.Context, key string) ([]byte, error) {
	r, err := s.GetReader(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// GetReader opens a streaming GET. 404 ⇒ "file not found" matching
// LocalStorageBackend / Cloudreve adapters.
func (s *S3StorageBackend) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	objectKey, err := s.keyFor(key)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, fmt.Errorf("s3 storage: file not found: %s", key)
		}
		return nil, fmt.Errorf("s3 storage: GetObject %q: %w", objectKey, err)
	}
	return result.Body, nil
}

// Delete removes the object. Per AWS docs S3 silently succeeds on missing
// keys, so no special-case for 404 is needed.
func (s *S3StorageBackend) Delete(ctx context.Context, key string) error {
	objectKey, err := s.keyFor(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: DeleteObject %q: %w", objectKey, err)
	}
	return nil
}

// Exists uses HeadObject (cheap HEAD). 404 ⇒ false without error.
func (s *S3StorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	objectKey, err := s.keyFor(key)
	if err != nil {
		return false, err
	}
	_, err = s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("s3 storage: HeadObject %q: %w", objectKey, err)
	}
	return true, nil
}

// GetMetadata uses HeadObject and maps to the canonical FileMetadata.
func (s *S3StorageBackend) GetMetadata(ctx context.Context, key string) (*FileMetadata, error) {
	objectKey, err := s.keyFor(key)
	if err != nil {
		return nil, err
	}
	result, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, fmt.Errorf("s3 storage: file not found: %s", key)
		}
		return nil, fmt.Errorf("s3 storage: HeadObject %q: %w", objectKey, err)
	}

	var size int64
	if result.ContentLength != nil {
		size = *result.ContentLength
	}
	var modTime time.Time
	if result.LastModified != nil {
		modTime = *result.LastModified
	}
	etag := strings.Trim(aws.ToString(result.ETag), `"`)

	return &FileMetadata{
		Key:          key,
		Size:         size,
		LastModified: modTime,
		ContentType:  aws.ToString(result.ContentType),
		ETag:         etag,
	}, nil
}

// List paginates ListObjectsV2 under the requested prefix. Returns storage
// keys (prefix stripped) so the rest of the package sees backend-agnostic paths.
func (s *S3StorageBackend) List(ctx context.Context, prefix string) ([]string, error) {
	cleaned := prefix
	if cleaned != "" {
		c, err := sanitizeKey(cleaned)
		if err != nil {
			return nil, err
		}
		cleaned = c
	}

	fullPrefix := s.prefix
	if cleaned != "" {
		if fullPrefix != "" {
			fullPrefix = fullPrefix + "/" + cleaned
		} else {
			fullPrefix = cleaned
		}
	}
	if fullPrefix != "" && !strings.HasSuffix(fullPrefix, "/") {
		fullPrefix += "/"
	}

	var keys []string
	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(fullPrefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 storage: ListObjectsV2: %w", err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			keys = append(keys, s.stripPrefix(*obj.Key))
		}
	}
	return keys, nil
}

// GetBackendType returns the canonical name used by StorageConfig.Type.
func (s *S3StorageBackend) GetBackendType() string {
	return "s3"
}

// HealthCheck calls HeadBucket — exercises creds + network + bucket policy.
// Cheaper than GetBucketInfo and avoids needing ListBucket permission.
func (s *S3StorageBackend) HealthCheck(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: HealthCheck failed: %w", err)
	}
	return nil
}

// isS3NotFound inspects any returned error and reports whether it represents
// a missing object. S3 returns *types.NotFound for missing objects.
func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	var nf *types.NotFound
	return errors.As(err, &nf)
}

// Compile-time check: S3StorageBackend implements StorageBackend. Catches
// signature drift early at `go build` time instead of at the first request.
var _ StorageBackend = (*S3StorageBackend)(nil)
