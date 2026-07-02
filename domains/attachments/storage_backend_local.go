package attachments

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalStorageBackend 本地文件系统存储后端
type LocalStorageBackend struct {
	baseDir string
	mu      sync.RWMutex

	// mkdirCache 缓存已创建的目录，避免重复 MkdirAll 系统调用
	mkdirCache sync.Map
}

// NewLocalStorageBackend 创建本地文件系统存储后端
// baseDir: 存储根目录的绝对路径
func NewLocalStorageBackend(baseDir string) (*LocalStorageBackend, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("local storage: baseDir cannot be empty")
	}

	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("local storage: resolve baseDir: %w", err)
	}

	// 确保根目录存在
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, fmt.Errorf("local storage: create baseDir: %w", err)
	}

	return &LocalStorageBackend{
		baseDir: abs,
	}, nil
}

// BaseDir 返回当前存储根目录（用于向后兼容）
func (b *LocalStorageBackend) BaseDir() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.baseDir
}

// SetBaseDir 切换存储根目录（用于运行时迁移，需要写锁）
func (b *LocalStorageBackend) SetBaseDir(dir string) error {
	if dir == "" {
		dir = "./data/attachments"
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("local storage: resolve dir: %w", err)
	}

	if err := os.MkdirAll(abs, 0755); err != nil {
		return fmt.Errorf("local storage: create dir: %w", err)
	}

	b.mu.Lock()
	b.baseDir = abs
	b.mu.Unlock()

	// 清空目录缓存，避免对旧目录的缓存误判新目录
	b.mkdirCache.Range(func(key, value interface{}) bool {
		b.mkdirCache.Delete(key)
		return true
	})

	return nil
}

// SaveFile 实现 StorageBackend 接口
func (b *LocalStorageBackend) SaveFile(relPath string, data []byte) error {
	start := time.Now()
	fullPath, err := b.safeJoin(relPath)
	if err != nil {
		recordOp("save", "local", start, err, 0)
		return err
	}

	// 确保父目录存在
	dir := filepath.Dir(fullPath)
	if err := b.ensureDir(dir); err != nil {
		recordOp("save", "local", start, err, 0)
		return fmt.Errorf("local storage: ensure dir: %w", err)
	}

	// 写入文件
	err = os.WriteFile(fullPath, data, 0644)
	recordOp("save", "local", start, err, int64(len(data)))
	if err != nil {
		return fmt.Errorf("local storage: write file: %w", err)
	}

	return nil
}

// LoadFile 实现 StorageBackend 接口
func (b *LocalStorageBackend) LoadFile(relPath string) ([]byte, error) {
	start := time.Now()
	fullPath, err := b.safeJoin(relPath)
	if err != nil {
		recordOp("load", "local", start, err, 0)
		return nil, err
	}

	data, err := os.ReadFile(fullPath)
	recordOp("load", "local", start, err, int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("local storage: read file: %w", err)
	}

	return data, nil
}

// FileExists 实现 StorageBackend 接口
func (b *LocalStorageBackend) FileExists(relPath string) (bool, error) {
	fullPath, err := b.safeJoin(relPath)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(fullPath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("local storage: stat file: %w", err)
}

// StatFile 实现 StorageBackend 接口
func (b *LocalStorageBackend) StatFile(relPath string) (*FileInfo, error) {
	fullPath, err := b.safeJoin(relPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("local storage: stat file: %w", err)
	}

	return &FileInfo{
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}, nil
}

// OpenStream 实现 StorageBackend 接口
func (b *LocalStorageBackend) OpenStream(relPath string) (io.ReadCloser, error) {
	fullPath, err := b.safeJoin(relPath)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, fmt.Errorf("local storage: open file: %w", err)
	}

	return f, nil
}

// DeleteFile 实现 StorageBackend 接口
func (b *LocalStorageBackend) DeleteFile(relPath string) error {
	fullPath, err := b.safeJoin(relPath)
	if err != nil {
		return err
	}

	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("local storage: delete file: %w", err)
	}

	return nil
}

// HealthCheck 实现 StorageBackend 接口
func (b *LocalStorageBackend) HealthCheck() error {
	start := time.Now()
	b.mu.RLock()
	base := b.baseDir
	b.mu.RUnlock()

	// 检查目录是否存在
	info, err := os.Stat(base)
	if err != nil {
		recordHealthCheck("local", start, err)
		return fmt.Errorf("local storage: base dir not accessible: %w", err)
	}

	if !info.IsDir() {
		err := fmt.Errorf("local storage: base dir is not a directory: %s", base)
		recordHealthCheck("local", start, err)
		return err
	}

	// 测试写入权限：创建临时文件
	testFile := filepath.Join(base, ".health_check_"+randomSuffix())
	f, err := os.Create(testFile)
	if err != nil {
		recordHealthCheck("local", start, err)
		return fmt.Errorf("local storage: cannot create test file (permission denied?): %w", err)
	}
	f.Close()
	os.Remove(testFile) // 清理测试文件

	recordHealthCheck("local", start, nil)
	return nil
}

// Info 实现 StorageBackend 接口
func (b *LocalStorageBackend) Info() BackendInfo {
	b.mu.RLock()
	base := b.baseDir
	b.mu.RUnlock()

	return BackendInfo{
		Type:     "local",
		Location: base,
		Metadata: map[string]string{
			"writable": "true",
		},
	}
}

// safeJoin 安全地拼接基础目录和相对路径，防止路径遍历攻击
func (b *LocalStorageBackend) safeJoin(relPath string) (string, error) {
	b.mu.RLock()
	base := b.baseDir
	b.mu.RUnlock()

	// 强制 relPath 为相对路径，消除 ".." 等危险路径
	cleaned := filepath.Clean("/" + relPath)
	full := filepath.Join(base, cleaned)

	// 二次校验：结果必须在 baseDir 之内
	absBase := filepath.Clean(base)
	if !strings.HasPrefix(filepath.Clean(full)+string(filepath.Separator), absBase+string(filepath.Separator)) &&
		filepath.Clean(full) != absBase {
		return "", fmt.Errorf("local storage: path escapes base dir: %q", relPath)
	}

	return full, nil
}

// ensureDir 确保目录存在（带缓存）
func (b *LocalStorageBackend) ensureDir(dir string) error {
	if _, ok := b.mkdirCache.Load(dir); ok {
		return nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	b.mkdirCache.Store(dir, true)
	return nil
}
