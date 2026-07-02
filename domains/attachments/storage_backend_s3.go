// +build !no_s3

package attachments

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3StorageBackend AWS S3/MinIO 存储后端
type S3StorageBackend struct {
	client *s3.Client
	bucket string
	prefix string // 基础路径前缀
}

// NewS3StorageBackend 创建 S3/MinIO 存储后端
func NewS3StorageBackend(cfg S3Config) (*S3StorageBackend, error) {
	if cfg.BucketName == "" {
		return nil, fmt.Errorf("s3 storage: bucket name cannot be empty")
	}
	if cfg.AccessKeyID == "" {
		return nil, fmt.Errorf("s3 storage: access key id cannot be empty")
	}
	if cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("s3 storage: secret access key cannot be empty")
	}

	ctx := context.Background()

	// 构建 AWS 配置
	var opts []func(*config.LoadOptions) error

	// 设置区域（MinIO 可以是任意值）
	region := cfg.Region
	if region == "" {
		region = "us-east-1" // 默认区域
	}
	opts = append(opts, config.WithRegion(region))

	// 设置静态凭证
	opts = append(opts, config.WithCredentialsProvider(
		credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"", // session token（可选）
		),
	))

	// 加载配置
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3 storage: load config: %w", err)
	}

	// 创建 S3 客户端
	var clientOpts []func(*s3.Options)

	// 自定义 endpoint（用于 MinIO 或私有 S3 兼容服务）
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = cfg.UsePathStyle
		})
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)

	return &S3StorageBackend{
		client: client,
		bucket: cfg.BucketName,
		prefix: cfg.BasePath,
	}, nil
}

// SaveFile 实现 StorageBackend 接口
func (b *S3StorageBackend) SaveFile(relPath string, data []byte) error {
	key := b.objectKey(relPath)

	_, err := b.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: put object: %w", err)
	}

	return nil
}

// LoadFile 实现 StorageBackend 接口
func (b *S3StorageBackend) LoadFile(relPath string) ([]byte, error) {
	key := b.objectKey(relPath)

	result, err := b.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 storage: get object: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("s3 storage: read object: %w", err)
	}

	return data, nil
}

// FileExists 实现 StorageBackend 接口
func (b *S3StorageBackend) FileExists(relPath string) (bool, error) {
	key := b.objectKey(relPath)

	_, err := b.client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// 检查是否是 NoSuchKey 错误
		var nsk *types.NoSuchKey
		var nsb *types.NotFound
		if err != nil {
			// 简化判断：包含这些字符串即认为对象不存在
			errStr := err.Error()
			if contains(errStr, "NoSuchKey") || contains(errStr, "NotFound") || contains(errStr, "404") {
				return false, nil
			}
		}
		// 使用类型断言检查
		if nsk != nil || nsb != nil {
			return false, nil
		}
		return false, fmt.Errorf("s3 storage: head object: %w", err)
	}

	return true, nil
}

// StatFile 实现 StorageBackend 接口
func (b *S3StorageBackend) StatFile(relPath string) (*FileInfo, error) {
	key := b.objectKey(relPath)

	result, err := b.client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 storage: head object: %w", err)
	}

	info := &FileInfo{}
	if result.ContentLength != nil {
		info.Size = *result.ContentLength
	}
	if result.LastModified != nil {
		info.ModTime = *result.LastModified
	}

	return info, nil
}

// OpenStream 实现 StorageBackend 接口
func (b *S3StorageBackend) OpenStream(relPath string) (io.ReadCloser, error) {
	key := b.objectKey(relPath)

	result, err := b.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 storage: get object stream: %w", err)
	}

	return result.Body, nil
}

// DeleteFile 实现 StorageBackend 接口
func (b *S3StorageBackend) DeleteFile(relPath string) error {
	key := b.objectKey(relPath)

	_, err := b.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: delete object: %w", err)
	}

	return nil
}

// HealthCheck 实现 StorageBackend 接口
func (b *S3StorageBackend) HealthCheck() error {
	ctx := context.Background()

	// 检查 bucket 是否存在并可访问
	_, err := b.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(b.bucket),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: bucket not accessible: %w", err)
	}

	// 测试写入权限：上传并删除一个小文件
	testKey := path.Join(b.prefix, ".health_check")
	testData := []byte("health check")

	_, err = b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(testKey),
		Body:   bytes.NewReader(testData),
	})
	if err != nil {
		return fmt.Errorf("s3 storage: cannot write test object (permission denied?): %w", err)
	}

	// 清理测试对象
	_, _ = b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(testKey),
	})

	return nil
}

// Info 实现 StorageBackend 接口
func (b *S3StorageBackend) Info() BackendInfo {
	metadata := map[string]string{
		"bucket": b.bucket,
	}

	if b.prefix != "" {
		metadata["prefix"] = b.prefix
	}

	// 尝试获取 bucket 区域
	ctx := context.Background()
	if region, err := b.client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{
		Bucket: aws.String(b.bucket),
	}); err == nil && region.LocationConstraint != "" {
		metadata["region"] = string(region.LocationConstraint)
	}

	location := "s3.amazonaws.com"
	if b.client.Options().BaseEndpoint != nil {
		location = *b.client.Options().BaseEndpoint
	}

	return BackendInfo{
		Type:     "s3",
		Location: location,
		Metadata: metadata,
	}
}

// objectKey 拼接对象键：prefix/relPath
func (b *S3StorageBackend) objectKey(relPath string) string {
	if b.prefix == "" {
		return relPath
	}
	return path.Join(b.prefix, relPath)
}

// contains 简单的字符串包含检查
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || hasSubstr(s, substr)))
}

func hasSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
