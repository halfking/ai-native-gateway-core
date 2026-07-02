package attachments

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StorageConfig 存储配置
type StorageConfig struct {
	Type string // 存储类型：local, oss, s3

	// Local 存储配置
	LocalDir string

	// OSS 存储配置
	OSSEndpoint        string
	OSSAccessKeyID     string
	OSSAccessKeySecret string
	OSSBucket          string
	OSSPrefix          string

	// S3 存储配置
	S3Endpoint        string
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3Bucket          string
	S3Prefix          string
	S3UseSSL          bool
	S3ForcePathStyle  bool
}

// NewStorageBackendFromConfig 根据配置创建存储后端
func NewStorageBackendFromConfig(config StorageConfig) (StorageBackend, error) {
	switch strings.ToLower(config.Type) {
	case "local", "":
		// 默认使用本地存储
		if config.LocalDir == "" {
			return nil, fmt.Errorf("local storage directory is required")
		}
		return NewLocalStorageBackend(config.LocalDir)

	case "oss":
		// 阿里云 OSS
		return NewOSSStorageBackend(&OSSConfig{
			Endpoint:        config.OSSEndpoint,
			AccessKeyID:     config.OSSAccessKeyID,
			AccessKeySecret: config.OSSAccessKeySecret,
			BucketName:      config.OSSBucket,
			BasePath:        config.OSSPrefix,
		})

	case "s3", "minio":
		// AWS S3 或 MinIO
		return NewS3StorageBackend(&S3Config{
			Endpoint:        config.S3Endpoint,
			Region:          config.S3Region,
			AccessKeyID:     config.S3AccessKeyID,
			SecretAccessKey: config.S3SecretAccessKey,
			BucketName:      config.S3Bucket,
			BasePath:        config.S3Prefix,
			UsePathStyle:    config.S3ForcePathStyle,
		})

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", config.Type)
	}
}

// LoadStorageConfigFromEnv 从环境变量加载存储配置
func LoadStorageConfigFromEnv() StorageConfig {
	storageType := getEnv("LLM_GATEWAY_STORAGE_TYPE", "local")

	config := StorageConfig{
		Type: storageType,
	}

	switch strings.ToLower(storageType) {
	case "local", "":
		config.LocalDir = getEnv("LLM_GATEWAY_ATTACHMENT_DIR", "./data/attachments")

	case "oss":
		config.OSSEndpoint = getEnv("LLM_GATEWAY_OSS_ENDPOINT", "")
		config.OSSAccessKeyID = getEnv("LLM_GATEWAY_OSS_ACCESS_KEY_ID", "")
		config.OSSAccessKeySecret = getEnv("LLM_GATEWAY_OSS_ACCESS_KEY_SECRET", "")
		config.OSSBucket = getEnv("LLM_GATEWAY_OSS_BUCKET", "")
		config.OSSPrefix = getEnv("LLM_GATEWAY_OSS_PREFIX", "attachments/")

	case "s3", "minio":
		config.S3Endpoint = getEnv("LLM_GATEWAY_S3_ENDPOINT", "")
		config.S3Region = getEnv("LLM_GATEWAY_S3_REGION", "us-east-1")
		config.S3AccessKeyID = getEnv("LLM_GATEWAY_S3_ACCESS_KEY_ID", "")
		config.S3SecretAccessKey = getEnv("LLM_GATEWAY_S3_SECRET_ACCESS_KEY", "")
		config.S3Bucket = getEnv("LLM_GATEWAY_S3_BUCKET", "")
		config.S3Prefix = getEnv("LLM_GATEWAY_S3_PREFIX", "attachments/")
		config.S3UseSSL = getEnv("LLM_GATEWAY_S3_USE_SSL", "true") == "true"
		config.S3ForcePathStyle = getEnv("LLM_GATEWAY_S3_FORCE_PATH_STYLE", "false") == "true"
	}

	return config
}

// getEnv 获取环境变量，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// ValidateStorageConfig 验证存储配置
func ValidateStorageConfig(config StorageConfig) error {
	switch strings.ToLower(config.Type) {
	case "local", "":
		if config.LocalDir == "" {
			return fmt.Errorf("local storage directory is required")
		}
		// 确保目录存在
		if err := os.MkdirAll(config.LocalDir, 0755); err != nil {
			return fmt.Errorf("failed to create local storage directory: %w", err)
		}

	case "oss":
		if config.OSSEndpoint == "" {
			return fmt.Errorf("OSS endpoint is required")
		}
		if config.OSSAccessKeyID == "" {
			return fmt.Errorf("OSS access key ID is required")
		}
		if config.OSSAccessKeySecret == "" {
			return fmt.Errorf("OSS access key secret is required")
		}
		if config.OSSBucket == "" {
			return fmt.Errorf("OSS bucket is required")
		}

	case "s3", "minio":
		if config.S3AccessKeyID == "" {
			return fmt.Errorf("S3 access key ID is required")
		}
		if config.S3SecretAccessKey == "" {
			return fmt.Errorf("S3 secret access key is required")
		}
		if config.S3Bucket == "" {
			return fmt.Errorf("S3 bucket is required")
		}
		if config.S3Region == "" {
			return fmt.Errorf("S3 region is required")
		}

	default:
		return fmt.Errorf("unsupported storage type: %s", config.Type)
	}

	return nil
}

// MigrateStorage 迁移存储（从一个后端迁移到另一个后端）
func MigrateStorage(ctx context.Context, fromBackend, toBackend StorageBackend) error {
	// 列出源后端的所有文件
	keys, err := fromBackend.List(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to list files from source backend: %w", err)
	}

	// 迁移每个文件
	for _, key := range keys {
		// 从源后端读取
		data, err := fromBackend.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("failed to load file %s from source backend: %w", key, err)
		}

		// 保存到目标后端
		if err := toBackend.Save(ctx, key, data); err != nil {
			return fmt.Errorf("failed to save file %s to destination backend: %w", key, err)
		}
	}

	return nil
}

// GetStorageStats 获取存储统计信息
type StorageStats struct {
	TotalFiles int64
	TotalSize  int64
	Backend    string
}

// GetStats 获取存储统计信息
func GetStats(ctx context.Context, backend StorageBackend, backendType string) (*StorageStats, error) {
	keys, err := backend.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	stats := &StorageStats{
		TotalFiles: int64(len(keys)),
		Backend:    backendType,
	}

	// 如果是本地存储，可以统计文件大小
	if localBackend, ok := backend.(*LocalStorageBackend); ok {
		var totalSize int64
		for _, key := range keys {
			filePath := filepath.Join(localBackend.baseDir, key)
			info, err := os.Stat(filePath)
			if err != nil {
				continue
			}
			totalSize += info.Size()
		}
		stats.TotalSize = totalSize
	}

	return stats, nil
}
