package attachments

import (
	"io"
	"time"
)

// StorageBackend 定义附件存储后端的抽象接口。
// 支持本地文件系统、阿里云 OSS、AWS S3/MinIO 等多种存储后端。
//
// 设计原则：
//  1. 接口方法签名与 Storage 的内部逻辑对齐
//  2. relPath 统一使用相对路径（如 "2026/07/req_xxx/abc123.png"）
//  3. 所有后端实现相同接口，便于切换
//  4. 后端只负责纯粹的读写操作，业务逻辑（base64 解码、哈希、去重）在 Storage 层
type StorageBackend interface {
	// SaveFile 保存文件到后端
	// relPath: 相对路径（如 "2026/07/req_xxx/abc123.png"）
	// data: 文件内容（已解码）
	// 返回 error 如果保存失败
	SaveFile(relPath string, data []byte) error

	// LoadFile 从后端加载文件内容
	// relPath: 相对路径
	// 返回文件内容和 error
	LoadFile(relPath string) ([]byte, error)

	// FileExists 检查文件是否存在
	// relPath: 相对路径
	// 返回 (存在标志, error)
	FileExists(relPath string) (bool, error)

	// StatFile 获取文件元信息（用于设置 Content-Length 等）
	// relPath: 相对路径
	// 返回文件信息和 error
	StatFile(relPath string) (*FileInfo, error)

	// OpenStream 打开文件流（用于大文件下载，避免一次性加载到内存）
	// relPath: 相对路径
	// 返回 ReadCloser 和 error
	// 调用方负责关闭 ReadCloser
	OpenStream(relPath string) (io.ReadCloser, error)

	// DeleteFile 删除文件（用于清理过期附件）
	// relPath: 相对路径
	// 返回 error 如果删除失败
	DeleteFile(relPath string) error
}

// FileInfo 文件元信息
type FileInfo struct {
	Size    int64     // 文件大小（字节）
	ModTime time.Time // 最后修改时间
}

// OSSConfig 阿里云 OSS 存储配置
type OSSConfig struct {
	// Endpoint OSS endpoint（如 oss-cn-hangzhou.aliyuncs.com）
	Endpoint string

	// AccessKeyID 访问密钥 ID
	AccessKeyID string

	// AccessKeySecret 访问密钥密文
	AccessKeySecret string

	// BucketName 存储桶名称
	BucketName string

	// BasePath 基础路径前缀（可选，用于在 bucket 内隔离不同环境）
	// 如 "attachments/prod"，最终对象键为 "attachments/prod/2026/07/req_xxx/abc.png"
	BasePath string

	// UseInternalEndpoint 是否使用内网 endpoint（默认 false）
	UseInternalEndpoint bool
}

// S3Config AWS S3/MinIO 存储配置
type S3Config struct {
	// Endpoint 自定义 endpoint（可选，MinIO 必填）
	// 如 "http://minio.example.com:9000"
	Endpoint string

	// Region AWS 区域（如 us-east-1）
	Region string

	// AccessKeyID 访问密钥 ID
	AccessKeyID string

	// SecretAccessKey 访问密钥密文
	SecretAccessKey string

	// BucketName 存储桶名称
	BucketName string

	// BasePath 基础路径前缀（可选）
	BasePath string

	// UsePathStyle 是否使用路径风格（MinIO 需要设为 true）
	// Path-style: http://endpoint/bucket/key
	// Virtual-hosted-style: http://bucket.endpoint/key
	UsePathStyle bool

	// UseSSL 是否使用 HTTPS（默认 true）
	UseSSL bool
}
