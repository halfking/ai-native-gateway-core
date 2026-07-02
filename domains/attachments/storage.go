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

// Storage 附件文件系统存储。零值不可用，必须通过 NewStorage 构造。
type Storage struct {
	// baseDir 存储根目录，绝对路径。通过 BaseDir()/SetBaseDir() 访问，
	// 受 dirMu 保护以支持运行时热切换（迁移到新目录后无需重启）。
	baseDir string
	// dirMu 保护 baseDir 与 mkdirCache 的并发读写。所有 baseDir 的读取
	// 走 BaseDir()（读锁），迁移时 SetBaseDir() 持写锁原子替换。
	dirMu sync.RWMutex
	// MaxSize 单文件最大字节数（解码后），0 表示用 DefaultMaxSize。
	// 该值在迁移时不变，无需加锁。
	MaxSize int64
	// mkdirCache 缓存已创建的目录，避免重复 MkdirAll 系统调用。
	// 注意：SetBaseDir 切换目录后必须清空此缓存，否则 ensureDir 会误以为
	// 新目录下的子目录已存在而跳过创建。
	mkdirCache sync.Map // map[string]bool
}

// NewStorage 构造一个 Storage。baseDir 为空时默认 ./data/attachments。
// 如果 baseDir 不存在会自动创建（0755）。
func NewStorage(baseDir string) (*Storage, error) {
	s := &Storage{MaxSize: DefaultMaxSize}
	if err := s.SetBaseDir(baseDir); err != nil {
		return nil, err
	}
	return s, nil
}

// BaseDir 返回当前存储根目录（绝对路径）。读锁，并发安全。
func (s *Storage) BaseDir() string {
	s.dirMu.RLock()
	defer s.dirMu.RUnlock()
	return s.baseDir
}

// SetBaseDir 热切换存储根目录。dir 为空时默认 ./data/attachments。
// 会 MkdirAll 新目录、清空 mkdirCache（避免对旧目录的缓存误判新目录）。
// 写锁，与并发的 SaveBase64Image/Load 互斥。
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
	if err := os.MkdirAll(abs, 0755); err != nil {
		return fmt.Errorf("attachments: create base dir %q: %w", abs, err)
	}
	s.dirMu.Lock()
	s.baseDir = abs
	// 切换目录后旧缓存失效：新目录下尚未创建 YYYY/MM/req_xxx 子目录，
	// 若不清空，ensureDir 会误判已创建而跳过 MkdirAll。
	s.mkdirCache.Range(func(k, _ any) bool { s.mkdirCache.Delete(k); return true })
	s.dirMu.Unlock()
	return nil
}

// Summary 统计当前存储目录的文件数、总字节数与最旧文件修改时间。
// 供管理端「文件系统占用」与迁移前后校验复用（收口重复的 WalkDir 逻辑）。
func (s *Storage) Summary() (fileCount int, totalBytes int64, oldestMod *time.Time, err error) {
	if s == nil {
		return 0, 0, nil, errors.New("attachments: storage is nil")
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

	// 目标目录：BaseDir/YYYY/MM/req_{requestID}
	now := time.Now()
	relDir := filepath.Join(
		fmt.Sprintf("%04d", now.Year()),
		fmt.Sprintf("%02d", int(now.Month())),
		fmt.Sprintf("req_%s", sanitizeRequestID(requestID)),
	)
	if err := s.ensureDir(relDir); err != nil {
		return nil, fmt.Errorf("attachments: ensure dir: %w", err)
	}

	// 流式解码：边解码边计算哈希，同时写入临时文件。
	// 这样无论文件多大，内存占用恒定（一个 chunk buffer）。
	ext := mimeTypeToExt(contentType)
	// 在整个保存流程内固定 baseDir 快照，避免迁移切换目录中途产生跨目录路径。
	baseDir := s.BaseDir()
	tmpPath := filepath.Join(baseDir, relDir, ".tmp_"+randomSuffix())
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("attachments: create tmp file: %w", err)
	}

	hasher := sha256.New()
	counter := &countingWriter{}
	// 用 base64 decoder 包裹一个 limited reader 防止超大输入耗尽内存/磁盘
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Payload))
	limited := io.LimitReader(decoder, maxSize+1)

	// tee: 同时写入临时文件和 hasher/counter
	tee := io.MultiWriter(f, io.MultiWriter(hasher, counter))
	if _, err := io.CopyBuffer(tee, limited, make([]byte, 64<<10)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath) // best-effort 清理临时文件
		return nil, fmt.Errorf("attachments: decode/write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("attachments: fsync: %w", err)
	}
	_ = f.Close()
	// 超过大小限制：删除临时文件并返回错误
	if counter.n > maxSize {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("attachments: file too large (%d > %d)", counter.n, maxSize)
	}

	hashHex := hex.EncodeToString(hasher.Sum(nil))
	fileName := hashHex[:16] + ext
	relPath := filepath.Join(relDir, fileName)
	fullPath := filepath.Join(baseDir, relPath)

	// 去重：目标文件已存在则直接删除临时文件
	deduped := false
	if _, statErr := os.Stat(fullPath); statErr == nil {
		_ = os.Remove(tmpPath)
		deduped = true
	} else if renameErr := os.Rename(tmpPath, fullPath); renameErr != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("attachments: rename tmp -> final: %w", renameErr)
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
	clean, err := s.safeJoin(relPath)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return nil, "", fmt.Errorf("attachments: read %q: %w", relPath, err)
	}
	return data, extToMimeType(filepath.Ext(relPath)), nil
}

// Stat 检查附件是否存在并返回文件信息。供 handler 设置 Content-Length。
func (s *Storage) Stat(relPath string) (os.FileInfo, error) {
	if s == nil {
		return nil, errors.New("attachments: storage is nil")
	}
	clean, err := s.safeJoin(relPath)
	if err != nil {
		return nil, err
	}
	return os.Stat(clean)
}

// OpenStream 打开附件用于流式读取（大文件）。调用方负责 Close。
func (s *Storage) OpenStream(relPath string) (io.ReadCloser, string, error) {
	if s == nil {
		return nil, "", errors.New("attachments: storage is nil")
	}
	clean, err := s.safeJoin(relPath)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, "", fmt.Errorf("attachments: open %q: %w", relPath, err)
	}
	return f, extToMimeType(filepath.Ext(relPath)), nil
}

// FullPath 返回相对路径对应的绝对路径。仅供内部/管理端使用。
func (s *Storage) FullPath(relPath string) (string, error) {
	return s.safeJoin(relPath)
}

// safeJoin 将相对路径安全拼接到 BaseDir，防止目录遍历（../../etc/passwd）。
// 每次调用快照当前 BaseDir，确保迁移切换目录后立即生效。
func (s *Storage) safeJoin(relPath string) (string, error) {
	base := s.BaseDir()
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

// ensureDir 创建 BaseDir/relDir 目录（带缓存，避免重复系统调用）。
func (s *Storage) ensureDir(relDir string) error {
	if _, ok := s.mkdirCache.Load(relDir); ok {
		return nil
	}
	full := filepath.Join(s.BaseDir(), relDir)
	if err := os.MkdirAll(full, 0755); err != nil {
		return err
	}
	s.mkdirCache.Store(relDir, true)
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
