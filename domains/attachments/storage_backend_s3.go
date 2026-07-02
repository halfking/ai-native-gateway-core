//go:build storage_s3

package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3StorageBackend 实现基于AWS S3或MinIO的存储后端
type S3StorageBackend struct {
	client   *s3.Client
	bucket   string
	prefix   string // 可选的对象键前缀
	endpoint string // MinIO或S3兼容服务的端点
}

// S3Config S3/MinIO存储配置
type S3Config struct {
	Endpoint        string // 留空使用AWS S3，填写则用于MinIO等兼容服务
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Prefix          string // 对象键前缀，如 "attachments/"
	UsePathStyle    bool   // MinIO通常需要设置为true
}

// NewS3StorageBackend 创建S3存储后端实例
func NewS3StorageBackend(cfg S3Config) (*S3StorageBackend, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("S3 bucket name is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1" // 默认区域
	}

	// 构建AWS配置
	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(cfg.Region))

	// 如果提供了访问密钥，使用静态凭证
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// 创建S3客户端
	clientOpts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		// 自定义端点（用于MinIO等）
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = cfg.UsePathStyle
		})
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)

	return &S3StorageBackend{
		client:   client,
		bucket:   cfg.Bucket,
		prefix:   strings.TrimSuffix(cfg.Prefix, "/"),
		endpoint: cfg.Endpoint,
	}, nil
}

// buildKey 构建完整的对象键
func (s *S3StorageBackend) buildKey(path string) string {
	path = strings.TrimPrefix(path, "/")
	if s.prefix == "" {
		return path
	}
	return s.prefix + "/" + path
}

// Save 保存文件到S3
func (s *S3StorageBackend) Save(ctx context.Context, path string, content []byte) error {
	key := s.buildKey(path)

	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(content),
		ContentLength: aws.Int64(int64(len(content))),
		ContentType:   aws.String(detectContentType(path)),
	})
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	return nil
}

// Load 从S3加载文件
func (s *S3StorageBackend) Load(ctx context.Context, path string) ([]byte, error) {
	key := s.buildKey(path)

	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object from S3: %w", err)
	}
	defer result.Body.Close()

	content, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read S3 object content: %w", err)
	}

	return content, nil
}

// Exists 检查S3对象是否存在
func (s *S3StorageBackend) Exists(ctx context.Context, path string) (bool, error) {
	key := s.buildKey(path)

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// 检查是否为NotFound错误
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check S3 object existence: %w", err)
	}

	return true, nil
}

// Delete 从S3删除文件
func (s *S3StorageBackend) Delete(ctx context.Context, path string) error {
	key := s.buildKey(path)

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete S3 object: %w", err)
	}

	return nil
}

// List 列出S3中指定前缀下的所有文件
func (s *S3StorageBackend) List(ctx context.Context, prefix string) ([]string, error) {
	fullPrefix := s.buildKey(prefix)
	if fullPrefix != "" && !strings.HasSuffix(fullPrefix, "/") {
		fullPrefix += "/"
	}

	var files []string
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(fullPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list S3 objects: %w", err)
		}

		for _, obj := range page.Contents {
			if obj.Key != nil {
				// 移除前缀，返回相对路径
				relPath := strings.TrimPrefix(*obj.Key, s.prefix+"/")
				files = append(files, relPath)
			}
		}
	}

	return files, nil
}

// GetMetadata 获取S3对象的元数据
func (s *S3StorageBackend) GetMetadata(ctx context.Context, path string) (*StorageMetadata, error) {
	key := s.buildKey(path)

	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 object metadata: %w", err)
	}

	size := int64(0)
	if result.ContentLength != nil {
		size = *result.ContentLength
	}

	modTime := time.Now()
	if result.LastModified != nil {
		modTime = *result.LastModified
	}

	return &StorageMetadata{
		Size:         size,
		ModifiedTime: modTime,
		ContentType:  aws.ToString(result.ContentType),
		ETag:         aws.ToString(result.ETag),
	}, nil
}

// GetURL 获取S3对象的预签名URL
func (s *S3StorageBackend) GetURL(ctx context.Context, path string, expiry time.Duration) (string, error) {
	key := s.buildKey(path)

	presignClient := s3.NewPresignClient(s.client)
	presignResult, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presignResult.URL, nil
}

// detectContentType 根据文件扩展名检测内容类型
func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	contentTypes := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
		".pdf":  "application/pdf",
		".txt":  "text/plain",
		".json": "application/json",
		".xml":  "application/xml",
		".zip":  "application/zip",
	}

	if ct, ok := contentTypes[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}
