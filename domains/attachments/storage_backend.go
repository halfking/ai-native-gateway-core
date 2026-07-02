package attachments

import (
	"context"
	"io"
	"time"
)

// StorageBackend 定义附件存储后端的抽象接口
// 支持本地文件系统、阿里云OSS、AWS S3等多种存储后端
type StorageBackend interface {
	// Save 保存文件内容到存储后端
	// 参数:
	//   - ctx: 上下文
	//   - key: 存储键（相对路径）
	//   - data: 文件内容
	// 返回:
	//   - error: 如果保存失败
	Save(ctx context.Context, key string, data []byte) error

	// Get 从存储后端读取文件内容
	// 参数:
	//   - ctx: 上下文
	//   - key: 存储键（相对路径）
	// 返回:
	//   - []byte: 文件内容
	//   - error: 如果读取失败或文件不存在
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete 从存储后端删除文件
	// 参数:
	//   - ctx: 上下文
	//   - key: 存储键（相对路径）
	// 返回:
	//   - error: 如果删除失败
	Delete(ctx context.Context, key string) error

	// Exists 检查文件是否存在
	// 参数:
	//   - ctx: 上下文
	//   - key: 存储键（相对路径）
	// 返回:
	//   - bool: 文件是否存在
	//   - error: 如果检查失败
	Exists(ctx context.Context, key string) (bool, error)

	// List 列出指定前缀下的所有文件
	// 参数:
	//   - ctx: 上下文
	//   - prefix: 前缀（例如: "2024/01/"）
	// 返回:
	//   - []string: 文件键列表
	//   - error: 如果列举失败
	List(ctx context.Context, prefix string) ([]string, error)

	// GetReader 获取文件内容的读取流
	// 用于大文件的流式读取
	// 参数:
	//   - ctx: 上下文
	//   - key: 存储键（相对路径）
	// 返回:
	//   - io.ReadCloser: 读取流（调用者负责关闭）
	//   - error: 如果获取失败
	GetReader(ctx context.Context, key string) (io.ReadCloser, error)

	// SaveReader 从读取流保存文件内容
	// 用于大文件的流式写入
	// 参数:
	//   - ctx: 上下文
	//   - key: 存储键（相对路径）
	//   - reader: 数据读取流
	//   - size: 数据大小（字节）
	// 返回:
	//   - error: 如果保存失败
	SaveReader(ctx context.Context, key string, reader io.Reader, size int64) error

	// GetMetadata 获取文件元数据
	// 参数:
	//   - ctx: 上下文
	//   - key: 存储键（相对路径）
	// 返回:
	//   - FileMetadata: 文件元数据
	//   - error: 如果获取失败
	GetMetadata(ctx context.Context, key string) (*FileMetadata, error)

	// GetBackendType 获取存储后端类型
	// 返回:
	//   - string: 后端类型（"filesystem", "oss", "s3"等）
	GetBackendType() string

	// HealthCheck 健康检查
	// 返回:
	//   - error: 如果后端不可用
	HealthCheck(ctx context.Context) error
}

// FileMetadata 文件元数据
type FileMetadata struct {
	Key          string    // 存储键
	Size         int64     // 文件大小（字节）
	LastModified time.Time // 最后修改时间
	ContentType  string    // 内容类型
	ETag         string    // ETag（如果可用）
}

// StorageBackendConfig 存储后端配置
type StorageBackendConfig struct {
	// Type 后端类型: "filesystem", "oss", "s3", "minio"
	Type string `json:"type"`

	// Filesystem 本地文件系统配置
	Filesystem *FilesystemConfig `json:"filesystem,omitempty"`

	// OSS 阿里云对象存储配置
	OSS *OSSConfig `json:"oss,omitempty"`

	// S3 AWS S3配置
	S3 *S3Config `json:"s3,omitempty"`
}

// FilesystemConfig 本地文件系统配置
type FilesystemConfig struct {
	// BaseDir 基础目录
	BaseDir string `json:"base_dir"`
}

// OSSConfig 阿里云对象存储配置
type OSSConfig struct {
	// Endpoint 访问域名
	Endpoint string `json:"endpoint"`

	// AccessKeyID 访问密钥ID
	AccessKeyID string `json:"access_key_id"`

	// AccessKeySecret 访问密钥密文
	AccessKeySecret string `json:"access_key_secret"`

	// BucketName 存储桶名称
	BucketName string `json:"bucket_name"`

	// BasePath 基础路径（可选，用于在bucket内隔离不同环境）
	BasePath string `json:"base_path,omitempty"`

	// UseInternalEndpoint 是否使用内网endpoint
	UseInternalEndpoint bool `json:"use_internal_endpoint"`
}

// S3Config AWS S3配置
type S3Config struct {
	// Region AWS区域
	Region string `json:"region"`

	// AccessKeyID 访问密钥ID
	AccessKeyID string `json:"access_key_id"`

	// SecretAccessKey 访问密钥密文
	SecretAccessKey string `json:"secret_access_key"`

	// BucketName 存储桶名称
	BucketName string `json:"bucket_name"`

	// BasePath 基础路径（可选）
	BasePath string `json:"base_path,omitempty"`

	// Endpoint 自定义endpoint（可选，用于MinIO等兼容S3的服务）
	Endpoint string `json:"endpoint,omitempty"`

	// UsePathStyle 是否使用路径风格（MinIO需要）
	UsePathStyle bool `json:"use_path_style"`
}
