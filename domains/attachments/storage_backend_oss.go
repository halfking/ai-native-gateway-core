// +build !no_oss

package attachments

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// OSSStorageBackend 阿里云 OSS 存储后端
type OSSStorageBackend struct {
	client *oss.Client
	bucket *oss.Bucket
	prefix string // 基础路径前缀
}

// NewOSSStorageBackend 创建阿里云 OSS 存储后端
func NewOSSStorageBackend(config OSSConfig) (*OSSStorageBackend, error) {
	if config.Endpoint == "" {
		return nil, fmt.Errorf("oss storage: endpoint cannot be empty")
	}
	if config.AccessKeyID == "" {
		return nil, fmt.Errorf("oss storage: access key id cannot be empty")
	}
	if config.AccessKeySecret == "" {
		return nil, fmt.Errorf("oss storage: access key secret cannot be empty")
	}
	if config.BucketName == "" {
		return nil, fmt.Errorf("oss storage: bucket name cannot be empty")
	}

	// 创建 OSS 客户端
	client, err := oss.New(config.Endpoint, config.AccessKeyID, config.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("oss storage: create client: %w", err)
	}

	// 获取 bucket
	bucket, err := client.Bucket(config.BucketName)
	if err != nil {
		return nil, fmt.Errorf("oss storage: get bucket: %w", err)
	}

	return &OSSStorageBackend{
		client: client,
		bucket: bucket,
		prefix: config.BasePath,
	}, nil
}

// SaveFile 实现 StorageBackend 接口
func (b *OSSStorageBackend) SaveFile(relPath string, data []byte) error {
	start := time.Now()
	objectKey := b.objectKey(relPath)
	
	err := b.bucket.PutObject(objectKey, bytes.NewReader(data))
	recordOp("save", "oss", start, err, int64(len(data)))
	if err != nil {
		return fmt.Errorf("oss storage: put object: %w", err)
	}

	return nil
}

// LoadFile 实现 StorageBackend 接口
func (b *OSSStorageBackend) LoadFile(relPath string) ([]byte, error) {
	start := time.Now()
	objectKey := b.objectKey(relPath)

	body, err := b.bucket.GetObject(objectKey)
	if err != nil {
		recordOp("load", "oss", start, err, 0)
		return nil, fmt.Errorf("oss storage: get object: %w", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	recordOp("load", "oss", start, err, int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("oss storage: read object: %w", err)
	}

	return data, nil
}

// FileExists 实现 StorageBackend 接口
func (b *OSSStorageBackend) FileExists(relPath string) (bool, error) {
	objectKey := b.objectKey(relPath)

	exists, err := b.bucket.IsObjectExist(objectKey)
	if err != nil {
		return false, fmt.Errorf("oss storage: check object exists: %w", err)
	}

	return exists, nil
}

// StatFile 实现 StorageBackend 接口
func (b *OSSStorageBackend) StatFile(relPath string) (*FileInfo, error) {
	objectKey := b.objectKey(relPath)

	meta, err := b.bucket.GetObjectMeta(objectKey)
	if err != nil {
		return nil, fmt.Errorf("oss storage: get object meta: %w", err)
	}

	// 从 header 中提取大小和修改时间
	sizeStr := meta.Get("Content-Length")
	modTimeStr := meta.Get("Last-Modified")

	var size int64
	if sizeStr != "" {
		fmt.Sscanf(sizeStr, "%d", &size)
	}

	var modTime time.Time
	if modTimeStr != "" {
		// OSS 返回的时间格式：Mon, 02 Jan 2006 15:04:05 GMT
		modTime, _ = time.Parse(time.RFC1123, modTimeStr)
	}

	return &FileInfo{
		Size:    size,
		ModTime: modTime,
	}, nil
}

// OpenStream 实现 StorageBackend 接口
func (b *OSSStorageBackend) OpenStream(relPath string) (io.ReadCloser, error) {
	objectKey := b.objectKey(relPath)

	body, err := b.bucket.GetObject(objectKey)
	if err != nil {
		return nil, fmt.Errorf("oss storage: get object stream: %w", err)
	}

	return body, nil
}

// DeleteFile 实现 StorageBackend 接口
func (b *OSSStorageBackend) DeleteFile(relPath string) error {
	objectKey := b.objectKey(relPath)

	err := b.bucket.DeleteObject(objectKey)
	if err != nil {
		return fmt.Errorf("oss storage: delete object: %w", err)
	}

	return nil
}

// HealthCheck 实现 StorageBackend 接口
func (b *OSSStorageBackend) HealthCheck() error {
	start := time.Now()
	
	// 检查 bucket 是否可访问 - 尝试列举对象（limit=1）
	lsRes, err := b.bucket.ListObjects(oss.MaxKeys(1))
	if err != nil {
		recordHealthCheck("oss", start, err)
		return fmt.Errorf("oss storage: bucket not accessible: %w", err)
	}
	_ = lsRes // 只要不报错就说明可访问

	// 测试写入权限：上传并删除一个小文件
	testKey := path.Join(b.prefix, ".health_check")
	testData := []byte("health check")
	
	if err := b.bucket.PutObject(testKey, bytes.NewReader(testData)); err != nil {
		recordHealthCheck("oss", start, err)
		return fmt.Errorf("oss storage: cannot write test object (permission denied?): %w", err)
	}

	// 清理测试对象
	_ = b.bucket.DeleteObject(testKey)

	recordHealthCheck("oss", start, nil)
	return nil
}

// Info 实现 StorageBackend 接口
func (b *OSSStorageBackend) Info() BackendInfo {
	metadata := map[string]string{
		"bucket": b.bucket.BucketName,
	}
	
	if b.prefix != "" {
		metadata["prefix"] = b.prefix
	}

	return BackendInfo{
		Type:     "oss",
		Location: b.client.Config.Endpoint,
		Metadata: metadata,
	}
}

// objectKey 拼接对象键：prefix/relPath
// 使用 path.Join 而非 filepath.Join，因为对象存储键总是用 "/" 分隔
func (b *OSSStorageBackend) objectKey(relPath string) string {
	if b.prefix == "" {
		return relPath
	}
	return path.Join(b.prefix, relPath)
}
