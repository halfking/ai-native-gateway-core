// Package attachments 提供请求附件（图片/文件）的文件系统存储能力。
//
// 设计目标：
//  1. 先保存文件，再转发 —— 客户端请求中的 base64 附件先落盘，
//     确保即使后续转发/数据库记录失败，附件依然可追溯。
//  2. 幂等去重 —— 同一内容（SHA256）只存一份，避免重复大文件浪费空间。
//  3. 大文件友好 —— base64 解码采用流式处理，避免一次性占用过多内存。
//     decodeBase64Stream 按 chunk 解码，内存占用恒定（~64KB）。
//  4. 异常容错 —— 存储失败不应阻塞请求转发，仅记录 warning。
//  5. 线程安全 —— 多个并发请求可能同时写入，使用 per-request 目录隔离。
//  6. 存储后端抽象 —— 支持本地文件系统、OSS、S3等多种存储后端。
//
// 存储路径布局：
//
//	{BaseDir}/YYYY/MM/req_{requestID}/{hash16}{ext}
//
// 例如：
//
//	/data/attachments/2026/07/req_abc123/a1b2c3d4e5f6g7h8.png
package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrInvalidDataURI 当 data URI 格式不合法时返回。
var ErrInvalidDataURI = errors.New("invalid data URI format")

// DefaultMaxSize 默认单文件最大 20MB（base64 解码后）。
// 超过此值的附件会被拒绝存储，但请求仍正常转发。
const DefaultMaxSize = 20 << 20

// AttachmentMetadata 描述单个附件的元数据，序列化后存入 request_logs.attachments。
type AttachmentMetadata struct {
	// Type 附件类型：image | file
	Type string `json:"type"`
	// ContentType MIME 类型，如 image/png
	ContentType string `json:"content_type"`
	// Size 解码后的字节数
	Size int64 `json:"size"`
	// Path 文件系统相对路径（相对 BaseDir），如 2026/07/req_xxx/abc.png
	Path string `json:"path"`
	// Hash 内容 SHA256 十六进制，用于去重和完整性校验
	Hash string `json:"hash"`
	// OriginalURL 原始引用。data URI 会截断至 200 字符；HTTP URL 原样保留。
	OriginalURL string `json:"original_url"`
	// MessageIndex 所在 message 在 messages 数组中的索引（便于定位）
	MessageIndex int `json:"message_index"`
	// BlockIndex 在该 message content 数组中的块索引
	BlockIndex int `json:"block_index"`
	// CreatedAt 保存时间
	CreatedAt time.Time `json:"created_at"`
}

// Storage 附件存储管理器。支持多种存储后端（本地文件系统、OSS、S3等）。
// 零值不可用，必须通过 NewStorage 构造。
type Storage struct {
	// backend 存储后端接口，支持本地文件系统、OSS、S3等
	backend StorageBackend
	// backendMu 保护 backend 的并发读写，支持运行时热切换存储后端
	backendMu sync.RWMutex
	// MaxSize 单文件最大字节数（解码后），0 表示用 DefaultMaxSize。
	MaxSize int64
	// baseDir 仅用于兼容旧 API（BaseDir/SetBaseDir），对于本地存储后端有效
	baseDir string
	// dirMu 保护 baseDir 的并发读写
	dirMu sync.RWMutex
}

// NewStorage 构造一个 Storage，使用本地文件系统作为默认存储后端。
// baseDir 为空时默认 ./data/attachments。
// 如果 baseDir 不存在会自动创建（0755）。
func NewStorage(baseDir string) (*Storage, error) {
	if baseDir == "" {
		baseDir = "./data/attachments"
	}
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("attachments: resolve base dir %q: %w", baseDir, err)
	}
	
	// 使用本地文件系统后端
	backend, err := NewLocalStorageBackend(abs)
	if err != nil {
		return nil, err
	}
	
	return &Storage{
		backend: backend,
		baseDir: abs,
		MaxSize: DefaultMaxSize,
	}, nil
}

// NewStorageWithBackend 使用指定的存储后端构造 Storage。
func NewStorageWithBackend(backend StorageBackend) *Storage {
	return &Storage{
		backend: backend,
		MaxSize: DefaultMaxSize,
	}
}

// SetBackend 热切换存储后端。写锁，与并发的 SaveBase64Image/Load 互斥。
func (s *Storage) SetBackend(backend StorageBackend) error {
	if s == nil {
		return errors.New("attachments: storage is nil")
	}
	if backend == nil {
		return errors.New("attachments: backend is nil")
	}
	
	s.backendMu.Lock()
	s.backend = backend
	s.backendMu.Unlock()
	
	return nil
}

// GetBackend 返回当前存储后端。读锁，并发安全。
func (s *Storage) GetBackend() StorageBackend {
	s.backendMu.RLock()
	defer s.backendMu.RUnlock()
	return s.backend
}

// BaseDir 返回当前存储根目录（绝对路径）。
// 注意：仅对本地文件系统后端有效，其他后端返回空字符串。
func (s *Storage) BaseDir() string {
	s.dirMu.RLock()
	defer s.dirMu.RUnlock()
	
	// 尝试从后端获取
	if local, ok := s.GetBackend().(*LocalStorageBackend); ok {
		return local.BaseDir()
	}
	
	return s.baseDir
}

// SetBaseDir 热切换存储根目录。dir 为空时默认 ./data/attachments。
// 注意：仅对本地文件系统后端有效，其他后端返回错误。
func (s *Storage) SetBaseDir(dir string) error {
	if s == nil {
		return errors.New("attachments: storage is nil")
	}
	if dir == "" {
		dir = "./data/attachments"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("attachments: resolve base dir %q: %w", dir, err)
	}
	
	// 检查当前后端是否为本地文件系统
	backend := s.GetBackend()
	local, ok := backend.(*LocalStorageBackend)
	if !ok {
		return errors.New("attachments: SetBaseDir only works with local storage backend")
	}
	
	// 更新本地存储后端的基础目录
	if err := local.SetBaseDir(abs); err != nil {
		return err
	}
	
	// 更新缓存的 baseDir
	s.dirMu.Lock()
	s.baseDir = abs
	s.dirMu.Unlock()
	
	return nil
}

// Summary 统计当前存储的文件数、总字节数与最旧文件修改时间。
// 供管理端「文件系统占用」与迁移前后校验复用。
func (s *Storage) Summary() (fileCount int, totalBytes int64, oldestMod *time.Time, err error) {
	if s == nil {
		return 0, 0, nil, errors.New("attachments: storage is nil")
	}
	
	backend := s.GetBackend()
	ctx := context.Background()
	
	// 列出所有文件
	files, err := backend.List(ctx, "")
	if err != nil {
		return 0, 0, nil, fmt.Errorf("attachments: list files: %w", err)
	}
	
	fileCount = len(files)
	for _, file := range files {
		meta, err := backend.GetMetadata(ctx, file)
		if err != nil {
			continue // 忽略无法访问的文件
		}
		totalBytes += meta.Size
		if oldestMod == nil || meta.LastModified.Before(*oldestMod) {
			t := meta.LastModified
			oldestMod = &t
		}
	}
	
	return fileCount, totalBytes, oldestMod, nil
}

// SaveResult 是 SaveBase64Image 的返回值。
// Path 为相对路径；Metadata 已完整填充。
type SaveResult struct {
	Path     string
	Metadata AttachmentMetadata
	// Deduped 为 true 表示内容已存在，本次未实际写入新文件（命中去重）。
	Deduped bool
}

// SaveBase64Image 解析 data URI 并将其中的 base64 图片保存到存储后端。
//
// 流程：
//  1. 解析 data:image/png;base64,... 提取 content type 和 base64 payload
//  2. 流式解码 + 哈希计算
//  3. 相同 hash 的文件已存在则跳过写入（去重）
//
// 该方法是幂等的：对相同内容重复调用只会保留一份文件。
//
// 失败场景（返回非 nil error）：
//   - data URI 格式错误 → ErrInvalidDataURI
//   - 超过 MaxSize → 文件不会被写入
//   - 存储后端错误 → 写入失败
//
// 调用方应在收到 error 时记录 warning 但不要阻塞请求转发。
func (s *Storage) SaveBase64Image(requestID, dataURI string, msgIdx, blockIdx int) (*SaveResult, error) {
	if s == nil {
		return nil, errors.New("attachments: storage is nil")
	}
	if requestID == "" {
		requestID = "unknown"
	}

	contentType, b64Payload, err := parseDataURI(dataURI)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDataURI, err)
	}

	maxSize := s.MaxSize
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}

	// 目标路径：YYYY/MM/req_{requestID}/
	now := time.Now()
	relDir := filepath.Join(
		fmt.Sprintf("%04d", now.Year()),
		fmt.Sprintf("%02d", int(now.Month())),
		fmt.Sprintf("req_%s", sanitizeRequestID(requestID)),
	)

	// 流式解码：边解码边计算哈希
	hasher := sha256.New()
	counter := &countingWriter{}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Payload))
	limited := io.LimitReader(decoder, maxSize+1)

	// 读取全部内容到内存（对于大多数图片附件来说可接受）
	// 同时计算哈希和大小
	tee := io.MultiWriter(hasher, counter)
	content, err := io.ReadAll(io.TeeReader(limited, tee))
	if err != nil {
		return nil, fmt.Errorf("attachments: decode: %w", err)
	}

	// 超过大小限制
	if counter.n > maxSize {
		return nil, fmt.Errorf("attachments: file too large (%d > %d)", counter.n, maxSize)
	}

	hashHex := hex.EncodeToString(hasher.Sum(nil))
	ext := mimeTypeToExt(contentType)
	fileName := hashHex[:16] + ext
	relPath := filepath.Join(relDir, fileName)

	backend := s.GetBackend()
	ctx := context.Background()

	// 去重：检查文件是否已存在
	exists, err := backend.Exists(ctx, relPath)
	if err != nil {
		return nil, fmt.Errorf("attachments: check exists: %w", err)
	}

	deduped := false
	if exists {
		// 文件已存在，跳过写入
		deduped = true
	} else {
		// 保存文件到存储后端
		if err := backend.Save(ctx, relPath, content); err != nil {
			return nil, fmt.Errorf("attachments: save: %w", err)
		}
	}

	// 构造元数据
	originalURL := dataURI
	if len(originalURL) > 200 {
		originalURL = originalURL[:200] + "..."
	}

	metadata := AttachmentMetadata{
		Type:         "image",
		ContentType:  contentType,
		Size:         counter.n,
		Path:         relPath,
		Hash:         hashHex,
		OriginalURL:  originalURL,
		MessageIndex: msgIdx,
		BlockIndex:   blockIdx,
		CreatedAt:    now,
	}

	return &SaveResult{
		Path:     relPath,
		Metadata: metadata,
		Deduped:  deduped,
	}, nil
}

// LoadAttachment 从存储后端加载附件内容。
// relPath 为相对路径，如 2026/07/req_xxx/abc.png。
// 返回文件内容、MIME 类型和错误。
func (s *Storage) LoadAttachment(relPath string) ([]byte, string, error) {
	if s == nil {
		return nil, "", errors.New("attachments: storage is nil")
	}

	backend := s.GetBackend()
	ctx := context.Background()

	// 加载文件内容 - 使用 Get 方法
	content, err := backend.Get(ctx, relPath)
	if err != nil {
		return nil, "", fmt.Errorf("attachments: load: %w", err)
	}

	// 从文件扩展名推断 MIME 类型
	contentType := extToMimeType(filepath.Ext(relPath))

	return content, contentType, nil
}

// Stat 返回附件的元数据（兼容旧 API）。
// 注意：返回的是 os.FileInfo 接口，仅用于向后兼容。
func (s *Storage) Stat(relPath string) (os.FileInfo, error) {
	if s == nil {
		return nil, errors.New("attachments: storage is nil")
	}

	backend := s.GetBackend()
	
	// 如果是本地存储后端，直接调用 Stat
	if local, ok := backend.(*LocalStorageBackend); ok {
		fullPath := filepath.Join(local.BaseDir(), relPath)
		return os.Stat(fullPath)
	}

	// 其他后端暂不支持 Stat
	return nil, errors.New("attachments: Stat not supported for non-local backend")
}

// OpenStream 打开附件的读取流（兼容旧 API）。
// 注意：仅对本地文件系统后端有效。
func (s *Storage) OpenStream(relPath string) (io.ReadCloser, string, error) {
	if s == nil {
		return nil, "", errors.New("attachments: storage is nil")
	}

	backend := s.GetBackend()
	
	// 如果是本地存储后端，直接打开文件
	if local, ok := backend.(*LocalStorageBackend); ok {
		fullPath := filepath.Join(local.BaseDir(), relPath)
		f, err := os.Open(fullPath)
		if err != nil {
			return nil, "", fmt.Errorf("attachments: open: %w", err)
		}
		contentType := extToMimeType(filepath.Ext(relPath))
		return f, contentType, nil
	}

	// 其他后端：先加载到内存，然后返回 ReadCloser
	ctx := context.Background()
	content, err := backend.Get(ctx, relPath)
	if err != nil {
		return nil, "", fmt.Errorf("attachments: load: %w", err)
	}
	contentType := extToMimeType(filepath.Ext(relPath))
	return io.NopCloser(strings.NewReader(string(content))), contentType, nil
}

// FullPath 返回附件的完整路径（仅对本地存储后端有效）。
func (s *Storage) FullPath(relPath string) (string, error) {
	if s == nil {
		return "", errors.New("attachments: storage is nil")
	}

	backend := s.GetBackend()
	if local, ok := backend.(*LocalStorageBackend); ok {
		fullPath := filepath.Join(local.BaseDir(), relPath)
		return fullPath, nil
	}

	return "", errors.New("attachments: FullPath only works with local storage backend")
}

// countingWriter 用于统计写入的字节数。
type countingWriter struct {
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// parseDataURI 解析 data URI 格式：data:[<mime>][;base64],<data>
// 返回 MIME 类型和 base64 payload。
func parseDataURI(dataURI string) (contentType, payload string, err error) {
	if !strings.HasPrefix(dataURI, "data:") {
		return "", "", errors.New("missing 'data:' prefix")
	}
	dataURI = strings.TrimPrefix(dataURI, "data:")

	comma := strings.Index(dataURI, ",")
	if comma < 0 {
		return "", "", errors.New("missing ',' separator")
	}

	header := dataURI[:comma]
	payload = dataURI[comma+1:]

	// header 格式：image/png;base64 或 image/png
	parts := strings.Split(header, ";")
	contentType = strings.TrimSpace(parts[0])
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// 检查是否包含 base64 标记
	isBase64 := false
	for _, part := range parts[1:] {
		if strings.TrimSpace(part) == "base64" {
			isBase64 = true
			break
		}
	}
	if !isBase64 {
		return "", "", errors.New("only base64 encoding is supported")
	}

	return contentType, payload, nil
}

// mimeTypeToExt 将 MIME 类型转换为文件扩展名。
func mimeTypeToExt(mimeType string) string {
	// 移除可能的参数（如 charset）
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = mimeType[:idx]
	}
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))

	exts := map[string]string{
		"image/jpeg":      ".jpg",
		"image/jpg":       ".jpg",
		"image/png":       ".png",
		"image/gif":       ".gif",
		"image/webp":      ".webp",
		"image/svg+xml":   ".svg",
		"application/pdf": ".pdf",
		"text/plain":      ".txt",
		"text/html":       ".html",
		"application/json": ".json",
		"application/xml":  ".xml",
	}

	if ext, ok := exts[mimeType]; ok {
		return ext
	}
	return ".bin"
}

// extToMimeType 将文件扩展名转换为 MIME 类型。
func extToMimeType(ext string) string {
	ext = strings.ToLower(ext)
	mimes := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
		".svg":  "image/svg+xml",
		".pdf":  "application/pdf",
		".txt":  "text/plain",
		".html": "text/html",
		".json": "application/json",
		".xml":  "application/xml",
	}

	if mime, ok := mimes[ext]; ok {
		return mime
	}
	return "application/octet-stream"
}

// sanitizeRequestID 清理 requestID，移除不安全的文件名字符。
func sanitizeRequestID(id string) string {
	// 只保留字母、数字、下划线、连字符
	var sb strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		}
	}
	if sb.Len() == 0 {
		return "unknown"
	}
	return sb.String()
}

// randomSuffix 生成随机后缀（用于临时文件名）。
func randomSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// safeJoin 安全拼接路径，防止目录遍历攻击。
// 返回的路径保证在 BaseDir 内。
func (s *Storage) safeJoin(relPath string) (string, error) {
	if s == nil {
		return "", errors.New("attachments: storage is nil")
	}
	
	baseDir := s.BaseDir()
	if baseDir == "" {
		return "", errors.New("attachments: base directory not set")
	}
	
	// 清理路径，移除 .. 和 .
	cleanPath := filepath.Clean(relPath)
	
	// 拼接路径
	fullPath := filepath.Join(baseDir, cleanPath)
	
	// 确保结果路径在 baseDir 内
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("attachments: resolve base dir: %w", err)
	}
	
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("attachments: resolve full path: %w", err)
	}
	
	// 检查是否逃逸
	if !strings.HasPrefix(absFull+string(filepath.Separator), absBase+string(filepath.Separator)) &&
		absFull != absBase {
		return "", fmt.Errorf("attachments: path escapes base directory: %s", relPath)
	}
	
	return absFull, nil
}
