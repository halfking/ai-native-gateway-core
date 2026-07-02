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
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// Storage 附件存储门面。零值不可用，必须通过 NewStorage 或 NewStorageWithBackend 构造。
// 内部使用可插拔的 StorageBackend，支持本地文件系统、OSS、S3 等多种存储后端。
type Storage struct {
	// backend 存储后端（本地文件系统、OSS、S3 等）
	backend StorageBackend
	
	// MaxSize 单文件最大字节数（解码后），0 表示用 DefaultMaxSize。
	MaxSize int64
	
	// 以下字段仅用于向后兼容和迁移支持
	// baseDir 仅当使用 LocalStorageBackend 时有效，用于 BaseDir()/SetBaseDir() API
	baseDir string
	dirMu   sync.RWMutex
	
	// mkdirCache 已废弃，由 LocalStorageBackend 内部管理
	mkdirCache sync.Map // map[string]bool
}

// NewStorage 构造一个 Storage，使用本地文件系统作为默认后端。
// baseDir 为空时默认 ./data/attachments。
// 如果 baseDir 不存在会自动创建（0755）。
func NewStorage(baseDir string) (*Storage, error) {
	if baseDir == "" {
		baseDir = "./data/attachments"
	}
	
	backend, err := NewLocalStorageBackend(baseDir)
	if err != nil {
		return nil, err
	}
	
	return &Storage{
		backend: backend,
		MaxSize: DefaultMaxSize,
		baseDir: backend.BaseDir(), // 缓存用于 BaseDir() API
	}, nil
}

// NewStorageWithBackend 使用指定的存储后端构造 Storage
// 用于支持 OSS、S3 等云存储后端
func NewStorageWithBackend(backend StorageBackend) *Storage {
	s := &Storage{
		backend: backend,
		MaxSize: DefaultMaxSize,
	}
	
	// 如果是本地后端，缓存 baseDir
	if local, ok := backend.(*LocalStorageBackend); ok {
		s.baseDir = local.BaseDir()
	}
	
	return s
}

// BaseDir 返回当前存储根目录（绝对路径）。读锁，并发安全。
func (s *Storage) BaseDir() string {
	s.dirMu.RLock()
	defer s.dirMu.RUnlock()
	return s.baseDir
}

// HealthCheck 执行存储后端健康检查
// 返回 error 如果后端不可用
func (s *Storage) HealthCheck() error {
	if s == nil || s.backend == nil {
		return errors.New("attachments: storage or backend is nil")
	}
	return s.backend.HealthCheck()
}

// BackendInfo 返回后端信息（类型、位置等）
func (s *Storage) BackendInfo() BackendInfo {
	if s == nil || s.backend == nil {
		return BackendInfo{Type: "unknown", Location: ""}
	}
	return s.backend.Info()
}

// SetBaseDir 热切换存储根目录。dir 为空时默认 ./data/attachments。
// 会 MkdirAll 新目录、清空 mkdirCache（避免对旧目录的缓存误判新目录）。
// 写锁，与并发的 SaveBase64Image/Load 互斥。
// 注意：仅当使用 LocalStorageBackend 时有效，云存储后端调用此方法返回错误。
func (s *Storage) SetBaseDir(dir string) error {
	if s == nil {
		return errors.New("attachments: storage is nil")
	}
	
	// 检查是否是本地存储后端
	local, ok := s.backend.(*LocalStorageBackend)
	if !ok {
		return errors.New("attachments: SetBaseDir only supported for local storage backend")
	}
	
	if dir == "" {
		dir = "./data/attachments"
	}
	
	// 调用后端的 SetBaseDir
	if err := local.SetBaseDir(dir); err != nil {
		return err
	}
	
	// 更新缓存的 baseDir
	s.dirMu.Lock()
	s.baseDir = local.BaseDir()
	// 切换目录后旧缓存失效：新目录下尚未创建 YYYY/MM/req_xxx 子目录，
	// 若不清空，ensureDir 会误判已创建而跳过 MkdirAll。
	s.mkdirCache.Range(func(k, _ any) bool { s.mkdirCache.Delete(k); return true })
	s.dirMu.Unlock()
	return nil
}

// Summary 统计当前存储目录的文件数、总字节数与最旧文件修改时间。
// 供管理端「文件系统占用」与迁移前后校验复用（收口重复的 WalkDir 逻辑）。
// 注意：仅当使用 LocalStorageBackend 时有效，云存储后端返回错误。
func (s *Storage) Summary() (fileCount int, totalBytes int64, oldestMod *time.Time, err error) {
	if s == nil {
		return 0, 0, nil, errors.New("attachments: storage is nil")
	}
	
	// 检查是否是本地存储后端
	if _, ok := s.backend.(*LocalStorageBackend); !ok {
		return 0, 0, nil, errors.New("attachments: Summary only supported for local storage backend")
	}
	
	base := s.BaseDir()
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 忽略无法访问的子目录
		}
		if d.IsDir() {
			return nil
		}
		fileCount++
		info, infoErr := d.Info()
		if infoErr == nil {
			totalBytes += info.Size()
			mt := info.ModTime()
			if oldestMod == nil || mt.Before(*oldestMod) {
				oldestMod = &mt
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, 0, nil, err
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

// SaveBase64Image 解析 data URI 并将其中的 base64 图片保存到文件系统。
//
// 流程：
//  1. 解析 data:image/png;base64,... 提取 content type 和 base64 payload
//  2. 流式解码 + 哈希计算 + 落盘（临时文件 -> rename，保证原子性）
//  3. 相同 hash 的文件已存在则跳过写入（去重）
//
// 该方法是幂等的：对相同内容重复调用只会保留一份文件。
//
// 失败场景（返回非 nil error）：
//   - data URI 格式错误 → ErrInvalidDataURI
//   - 超过 MaxSize → 文件不会被写入
//   - 磁盘满/权限不足 → 写入失败
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

	// 目标目录：YYYY/MM/req_{requestID}
	now := time.Now()
	relDir := filepath.Join(
		fmt.Sprintf("%04d", now.Year()),
		fmt.Sprintf("%02d", int(now.Month())),
		fmt.Sprintf("req_%s", sanitizeRequestID(requestID)),
	)

	// 流式解码：边解码边计算哈希，同时写入临时缓冲区。
	// 这样无论文件多大，内存占用恒定（一个 chunk buffer）。
	ext := mimeTypeToExt(contentType)
	
	hasher := sha256.New()
	counter := &countingWriter{}
	// 用 base64 decoder 包裹一个 limited reader 防止超大输入耗尽内存/磁盘
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Payload))
	limited := io.LimitReader(decoder, maxSize+1)

	// 解码到内存缓冲区，同时计算哈希和大小
	var buf bytes.Buffer
	tee := io.MultiWriter(&buf, io.MultiWriter(hasher, counter))
	if _, err := io.CopyBuffer(tee, limited, make([]byte, 64<<10)); err != nil {
		return nil, fmt.Errorf("attachments: decode: %w", err)
	}
	
	// 超过大小限制：返回错误
	if counter.n > maxSize {
		return nil, fmt.Errorf("attachments: file too large (%d > %d)", counter.n, maxSize)
	}

	hashHex := hex.EncodeToString(hasher.Sum(nil))
	fileName := hashHex[:16] + ext
	relPath := filepath.Join(relDir, fileName)
	
	// 去重：检查目标文件是否已存在
	deduped := false
	exists, err := s.backend.FileExists(relPath)
	if err != nil {
		return nil, fmt.Errorf("attachments: check exists: %w", err)
	}
	
	if exists {
		deduped = true
	} else {
		// 保存文件到后端
		if err := s.backend.SaveFile(relPath, buf.Bytes()); err != nil {
			return nil, fmt.Errorf("attachments: save file: %w", err)
		}
	}

	meta := AttachmentMetadata{
		Type:         "image",
		ContentType:  contentType,
		Size:         counter.n,
		Path:         filepath.ToSlash(relPath),
		Hash:         hashHex,
		OriginalURL:  truncateForLog(dataURI, 200),
		MessageIndex: msgIdx,
		BlockIndex:   blockIdx,
		CreatedAt:    now,
	}
	return &SaveResult{Path: filepath.ToSlash(relPath), Metadata: meta, Deduped: deduped}, nil
}

// LoadAttachment 读取附件内容，返回 (数据, contentType, error)。
// relPath 必须是相对路径（来自 AttachmentMetadata.Path）。
func (s *Storage) LoadAttachment(relPath string) ([]byte, string, error) {
	if s == nil {
		return nil, "", errors.New("attachments: storage is nil")
	}
	
	data, err := s.backend.LoadFile(relPath)
	if err != nil {
		return nil, "", fmt.Errorf("attachments: load %q: %w", relPath, err)
	}
	return data, extToMimeType(filepath.Ext(relPath)), nil
}

// Stat 检查附件是否存在并返回文件信息。供 handler 设置 Content-Length。
func (s *Storage) Stat(relPath string) (os.FileInfo, error) {
	if s == nil {
		return nil, errors.New("attachments: storage is nil")
	}
	
	info, err := s.backend.StatFile(relPath)
	if err != nil {
		return nil, fmt.Errorf("attachments: stat %q: %w", relPath, err)
	}
	
	// 将 FileInfo 转换为 os.FileInfo（适配器模式）
	return &fileInfoAdapter{info: info, name: filepath.Base(relPath)}, nil
}

// OpenStream 打开附件用于流式读取（大文件）。调用方负责 Close。
func (s *Storage) OpenStream(relPath string) (io.ReadCloser, string, error) {
	if s == nil {
		return nil, "", errors.New("attachments: storage is nil")
	}
	
	stream, err := s.backend.OpenStream(relPath)
	if err != nil {
		return nil, "", fmt.Errorf("attachments: open stream %q: %w", relPath, err)
	}
	return stream, extToMimeType(filepath.Ext(relPath)), nil
}

// FullPath 返回相对路径对应的绝对路径。仅供内部/管理端使用。
// 注意：仅当使用 LocalStorageBackend 时有效，云存储后端返回错误。
func (s *Storage) FullPath(relPath string) (string, error) {
	if _, ok := s.backend.(*LocalStorageBackend); !ok {
		return "", errors.New("attachments: FullPath only supported for local storage backend")
	}
	return s.safeJoin(relPath)
}

// safeJoin 将相对路径安全拼接到 BaseDir，防止目录遍历（../../etc/passwd）。
// 每次调用快照当前 BaseDir，确保迁移切换目录后立即生效。
// 注意：仅用于本地存储后端。
func (s *Storage) safeJoin(relPath string) (string, error) {
	base := s.BaseDir()
	if base == "" {
		return "", errors.New("attachments: baseDir is empty (not a local storage backend)")
	}
	cleaned := filepath.Clean("/" + relPath) // 强制为根绝对路径，消除 ..
	full := filepath.Join(base, cleaned)
	// 二次校验：结果必须在 BaseDir 之内
	absBase := filepath.Clean(base)
	if !strings.HasPrefix(filepath.Clean(full)+string(filepath.Separator), absBase+string(filepath.Separator)) &&
		filepath.Clean(full) != absBase {
		return "", fmt.Errorf("attachments: path escapes base dir: %q", relPath)
	}
	return full, nil
}

// ensureDir 已废弃，由 LocalStorageBackend 内部管理
// 保留此方法仅为向后兼容
func (s *Storage) ensureDir(relDir string) error {
	// 对于本地存储，目录创建由 backend 自动处理
	// 对于云存储，不需要创建目录
	return nil
}

// ─── data URI 解析 ──────────────────────────────────────────────

// parseDataURI 解析 "data:image/png;base64,iVBOR..." 格式。
// 返回 (contentType, base64Payload, error)。
// 不支持 base64 之外的编码（如 data:text/plain,... 纯文本），会返回错误。
func parseDataURI(uri string) (string, string, error) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", fmt.Errorf("missing data: prefix")
	}
	body := uri[len(prefix):]
	comma := strings.IndexByte(body, ',')
	if comma < 0 {
		return "", "", fmt.Errorf("missing comma separator")
	}
	header := body[:comma]
	payload := body[comma+1:]

	contentType := "application/octet-stream"
	isBase64 := false
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "base64" {
			isBase64 = true
		} else if part != "" && !strings.Contains(part, "=") {
			// 形如 image/png 的部分是 MIME type
			// 含 "=" 的（如 charset=utf-8）忽略
			if strings.Contains(part, "/") {
				contentType = part
			}
		}
	}
	if !isBase64 {
		return "", "", fmt.Errorf("only base64-encoded data URIs are supported")
	}
	if payload == "" {
		return "", "", fmt.Errorf("empty payload")
	}
	return contentType, payload, nil
}

// ─── 工具函数 ───────────────────────────────────────────────────

// countingWriter 统计写入字节数（用于大小校验）。
type countingWriter struct {
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func mimeTypeToExt(mt string) string {
	switch strings.ToLower(mt) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		// 未知类型：用 .bin，保留 content_type 在元数据中
		return ".bin"
	}
}

func extToMimeType(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// truncateForLog 将超长字符串截断（用于 OriginalURL 字段）。
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

// sanitizeRequestID 只保留 requestID 中的安全字符，防止路径注入。
func sanitizeRequestID(id string) string {
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// randomSuffix 生成临时文件后缀，避免并发写入冲突。
func randomSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// fileInfoAdapter 适配 FileInfo 到 os.FileInfo 接口
type fileInfoAdapter struct {
	info *FileInfo
	name string
}

func (a *fileInfoAdapter) Name() string       { return a.name }
func (a *fileInfoAdapter) Size() int64        { return a.info.Size }
func (a *fileInfoAdapter) Mode() fs.FileMode  { return 0644 }
func (a *fileInfoAdapter) ModTime() time.Time { return a.info.ModTime }
func (a *fileInfoAdapter) IsDir() bool        { return false }
func (a *fileInfoAdapter) Sys() interface{}   { return nil }
