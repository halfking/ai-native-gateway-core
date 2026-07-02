package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StorageManager 统一的存储管理器，使用可插拔的存储后端
type StorageManager struct {
	backend StorageBackend
}

// NewStorageManager 创建存储管理器实例
func NewStorageManager(backend StorageBackend) *StorageManager {
	return &StorageManager{
		backend: backend,
	}
}

// SaveAttachment 保存附件，返回附件的存储路径
// 使用内容寻址（content-addressing）确保相同内容只存储一次
func (sm *StorageManager) SaveAttachment(ctx context.Context, filename string, data []byte) (string, error) {
	// 计算文件内容的 SHA256 哈希值
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	// 使用哈希值的前两个字符作为分片目录，避免单个目录文件过多
	shardDir := hashStr[:2]

	// 构建存储路径：shard/hash.ext
	ext := getFileExtension(filename)
	storageKey := path.Join(shardDir, hashStr+ext)

	// 检查文件是否已存在（去重）
	exists, err := sm.backend.Exists(ctx, storageKey)
	if err != nil {
		return "", fmt.Errorf("failed to check file existence: %w", err)
	}

	// 如果文件已存在，直接返回路径
	if exists {
		return storageKey, nil
	}

	// 保存文件
	if err := sm.backend.Save(ctx, storageKey, data); err != nil {
		return "", fmt.Errorf("failed to save attachment: %w", err)
	}

	return storageKey, nil
}

// LoadAttachment 加载附件内容
func (sm *StorageManager) LoadAttachment(ctx context.Context, storageKey string) ([]byte, error) {
	data, err := sm.backend.Get(ctx, storageKey)
	if err != nil {
		return nil, fmt.Errorf("failed to load attachment: %w", err)
	}
	return data, nil
}

// GetAttachmentReader 获取附件的读取器，适用于大文件流式读取
func (sm *StorageManager) GetAttachmentReader(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	reader, err := sm.backend.GetReader(ctx, storageKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get attachment reader: %w", err)
	}
	return reader, nil
}

// DeleteAttachment 删除附件
func (sm *StorageManager) DeleteAttachment(ctx context.Context, storageKey string) error {
	if err := sm.backend.Delete(ctx, storageKey); err != nil {
		return fmt.Errorf("failed to delete attachment: %w", err)
	}
	return nil
}

// AttachmentExists 检查附件是否存在
func (sm *StorageManager) AttachmentExists(ctx context.Context, storageKey string) (bool, error) {
	exists, err := sm.backend.Exists(ctx, storageKey)
	if err != nil {
		return false, fmt.Errorf("failed to check attachment existence: %w", err)
	}
	return exists, nil
}

// ListAttachmentsByPrefix 列出指定前缀下的所有附件
func (sm *StorageManager) ListAttachmentsByPrefix(ctx context.Context, prefix string) ([]string, error) {
	keys, err := sm.backend.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list attachments: %w", err)
	}
	return keys, nil
}

// CleanupOrphanedAttachments 清理孤立的附件（没有被任何记录引用的附件）
// 这个方法需要配合数据库查询使用，传入仍在使用的附件键列表
func (sm *StorageManager) CleanupOrphanedAttachments(ctx context.Context, usedKeys map[string]bool) (int, error) {
	// 列出所有附件
	allKeys, err := sm.backend.List(ctx, "")
	if err != nil {
		return 0, fmt.Errorf("failed to list all attachments: %w", err)
	}

	// 找出未使用的附件
	var orphanedKeys []string
	for _, key := range allKeys {
		if !usedKeys[key] {
			orphanedKeys = append(orphanedKeys, key)
		}
	}

	// 删除孤立的附件
	deletedCount := 0
	for _, key := range orphanedKeys {
		if err := sm.backend.Delete(ctx, key); err != nil {
			// 记录错误但继续处理其他文件
			continue
		}
		deletedCount++
	}

	return deletedCount, nil
}

// CleanupOldAttachments 清理旧的附件（基于时间）
// 注意：这需要存储后端支持获取文件元数据（创建时间）
// 对于不支持元数据的后端，建议在数据库层面维护附件的创建时间
func (sm *StorageManager) CleanupOldAttachments(ctx context.Context, olderThan time.Time, keysToDelete []string) (int, error) {
	deletedCount := 0
	for _, key := range keysToDelete {
		if err := sm.backend.Delete(ctx, key); err != nil {
			// 记录错误但继续处理其他文件
			continue
		}
		deletedCount++
	}

	return deletedCount, nil
}

// GenerateUploadToken 生成上传令牌（用于前端直传等场景）
func (sm *StorageManager) GenerateUploadToken(ctx context.Context, filename string) (string, error) {
	// 生成唯一的上传令牌
	token := uuid.New().String()
	
	// TODO: 将令牌与文件名关联，存储到临时存储或缓存中
	// 这里需要配合 Redis 或其他缓存系统使用
	
	return token, nil
}

// getFileExtension 获取文件扩展名
func getFileExtension(filename string) string {
	// 查找最后一个点的位置
	lastDot := strings.LastIndex(filename, ".")
	if lastDot == -1 || lastDot == len(filename)-1 {
		return ""
	}
	return filename[lastDot:]
}
