// Package largebody stores immutable request bodies without retaining them in
// the Go heap. References are scoped to a trusted tenant and session identity.
package largebody

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const LimitBytes int64 = 100_000_000

// MaxRangeBytes bounds explicit materialization even when a caller accidentally
// supplies the full ingress limit. Larger readers must use Open and stream.
const MaxRangeBytes int64 = 8 << 20

var (
	ErrTooLarge  = errors.New("largebody: request exceeds 100000000 bytes")
	ErrExists    = errors.New("largebody: immutable request already exists")
	ErrIntegrity = errors.New("largebody: body reference integrity mismatch")
	ErrRange     = errors.New("largebody: invalid or excessive range")
)

type Identity struct {
	TenantID  string `json:"tenant_id"`
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
}

type Ref struct {
	Identity
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Span addresses bytes in the original, transfer-decoded business JSON body.
// Offsets produced from a SectionReader are relative to that section's start.
type Span struct{ Offset, Length int64 }

func (r Ref) RelativePath() string { return filepath.Join(requestDir(r.Identity), "raw.json") }

type Store struct {
	root *os.Root
	path string
}

func NewStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("largebody: empty store root")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("largebody: root must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Store{root: root, path: path}, nil
}

func (s *Store) RootPath() string { return s.path }
func (s *Store) Close() error     { return s.root.Close() }

func requestDir(id Identity) string {
	return filepath.Join(id.TenantID, id.SessionID[:min(2, len(id.SessionID))], id.SessionID, "large-v1", "requests", id.RequestID)
}

// Capture copies at most the hard limit plus one byte with a 64KiB buffer.
// The caller may pass an io.MultiReader(prefix, networkBody); length headers
// never participate in the limit decision. A reference is acknowledged only
// after the RAW and its small recovery record have both been synced.
func (s *Store) Capture(ctx context.Context, id Identity, r io.Reader) (ref Ref, err error) {
	if err = ctx.Err(); err != nil {
		return ref, err
	}
	if id.RequestID == "" {
		id.RequestID, err = randomID()
		if err != nil {
			return ref, err
		}
	}
	if err = validIdentity(id); err != nil {
		return ref, err
	}
	dir := requestDir(id)
	if err = s.ensureDirs(dir); err != nil {
		return ref, err
	}
	token, err := randomID()
	if err != nil {
		return ref, err
	}
	tmp := filepath.Join(dir, ".raw-"+token)
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ref, err
	}
	defer func() { _ = f.Close(); _ = s.root.Remove(tmp) }()
	
	// Wrap with frame-based compression
	fw, err := NewFrameWriter(f)
	if err != nil {
		return ref, err
	}
	
	h := sha256.New()
	// Hash the uncompressed data, compress writes to disk
	n, err := io.CopyBuffer(io.MultiWriter(fw, h), io.LimitReader(contextReader{ctx, r}, LimitBytes+1), make([]byte, 64<<10))
	if err != nil {
		return ref, err
	}
	if n > LimitBytes {
		return ref, ErrTooLarge
	}
	if err = ctx.Err(); err != nil {
		return ref, err
	}
	
	// Close FrameWriter to flush final frame
	if err = fw.Close(); err != nil {
		return ref, err
	}
	
	if err = f.Close(); err != nil {
		return ref, err
	}
	ref = Ref{Identity: id, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	final := ref.RelativePath()
	// Link provides atomic no-replace placement. Rename alone could replace
	// another request's supposedly immutable file on POSIX.
	if err = s.root.Link(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Ref{}, ErrExists
		}
		return Ref{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.root.Remove(final)
		}
	}()
	if err = s.writeRef(dir, ref); err != nil {
		return Ref{}, err
	}
	if err = s.syncDir(dir); err != nil {
		_ = s.root.Remove(filepath.Join(dir, "ref.json"))
		return Ref{}, err
	}
	committed = true
	return ref, nil
}

func (s *Store) writeRef(dir string, ref Ref) error {
	data, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	token, err := randomID()
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".ref-"+token)
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = s.root.Remove(tmp) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = s.root.Link(tmp, filepath.Join(dir, "ref.json")); errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	return err
}

func (s *Store) ensureDirs(path string) error {
	var current string
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		err := s.root.Mkdir(current, 0700)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err == nil {
			if err = s.syncDir(filepath.Dir(current)); err != nil {
				return err
			}
		}
		info, err := s.root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("largebody: symlink or non-directory in request path")
		}
	}
	return nil
}

func (s *Store) checkPath(path string) error {
	var current string
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := s.root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("largebody: symlink in body reference path")
		}
	}
	return nil
}

// File provides read access to an archived body with optional compression.
type File interface {
	io.Reader
	io.ReaderAt
	io.Closer
}

// Open does not read or hash the complete file. It validates trusted reference
// shape and size; call Verify or Recover before trusting a persisted record.
// Returns a reader that transparently decompresses if the file is compressed.
func (s *Store) Open(ctx context.Context, ref Ref) (File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validRef(ref); err != nil {
		return nil, err
	}
	path := ref.RelativePath()
	if err := s.checkPath(path); err != nil {
		return nil, err
	}
	f, err := s.root.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrIntegrity
	}
	
	// Check if file is compressed
	compressed, err := IsCompressed(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	
	// Seek back to start after magic check
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	
	if compressed {
		// Wrap with decompression
		fr, err := NewFrameReader(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return &compressedFile{fr: fr, f: f}, nil
	}
	
	// Legacy uncompressed file - verify size matches
	if info.Size() != ref.Size {
		_ = f.Close()
		return nil, ErrIntegrity
	}
	return f, nil
}

// compressedFile wraps FrameReader and underlying file for proper cleanup
type compressedFile struct {
	fr     *FrameReader
	f      *os.File
	pos    int64 // current read position for ReadAt emulation
	mu     sync.Mutex

	// dir 是帧目录（每帧在压缩文件 / 解压后流中的位置），首次需要跳转时
	// 懒建、之后只读。dirBuilt 区分「没建过」与「建了但为空」。
	dir      []frameDirEntry
	dirBuilt bool
	dirErr   error
}

// frameDirEntry 是一帧的位置索引：压缩文件里的帧头偏移，与它在解压后流里
// 覆盖的字节区间 [uncompOff, uncompEnd)。
type frameDirEntry struct {
	compOff   int64
	uncompOff int64
	uncompLen int64
	uncompEnd int64
}

func (cf *compressedFile) Read(p []byte) (int, error) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	n, err := cf.fr.Read(p)
	cf.pos += int64(n)
	return n, err
}

// ReadAt 实现压缩归档上的定位读取。
//
// ★ 2026-11-22（211 生产事故根修）：此前「往回读」一律**回卷到文件开头重新
// 解压到目标位置**，「往前读」一律把中间的字节全部解压后丢弃——注释自述
// "inefficient for random access but maintains correctness"。代价是 O(偏移)
// 而不是 O(一帧)：归约器（sessioncontext.Reduce）对同一条消息要开 3 个以上
// 独立 reader（历史守卫预筛 + 两个文本视图，每规则组各一轮），每次新开
// reader 都是一次「往回读」⇒ 3953 条消息 × 约 8 次回卷 × 平均解压 3.8MB
// ⇒ 7.7MB 的请求体归约耗时 141 秒（211 真机 profile：95.7% 在本函数），
// 撞破 LargeIngress 的 120 秒预算 ⇒ 503 request_processing_timeout。
//
// ⇒ 帧格式本就是「每 64KiB 一帧、帧帧独立」（见包注释 "enabling random-access
// reads"），这里把缺失的那一半补上：
//   - 回退、或大跨步前进（超过一帧）⇒ 经帧目录直接跳到目标所在的帧头；
//   - 小幅前进（一帧以内）⇒ 维持顺序丢弃（纯顺序访问零额外开销，也不建目录）。
//
// 目录只读各帧 12 字节帧头、不解压；建不出来（流不可定位 / 帧头损坏）时
// 退回旧行为（从文件头重放），正确性不变。
func (cf *compressedFile) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("largebody: negative read offset %d", off)
	}
	cf.mu.Lock()
	defer cf.mu.Unlock()

	if off < cf.pos || off-cf.pos > frameSize {
		if err := cf.jumpLocked(off); err != nil {
			return 0, err
		}
	}

	// Seek forward if needed
	if off > cf.pos {
		toDiscard := off - cf.pos
		if _, err := io.CopyN(io.Discard, cf.fr, toDiscard); err != nil {
			return 0, err
		}
		cf.pos = off
	}

	// Read the requested data
	n, err := io.ReadFull(cf.fr, p)
	cf.pos += int64(n)
	return n, err
}

// jumpLocked 把顺序游标重定位到覆盖 off 的那一帧的帧头（调用方持锁）。
func (cf *compressedFile) jumpLocked(off int64) error {
	target, newPos := int64(headerSize), int64(0)
	if dir, err := cf.frameDirLocked(); err == nil && len(dir) > 0 {
		i := sort.Search(len(dir), func(i int) bool { return dir[i].uncompEnd > off })
		if i == len(dir) {
			// 越过归档末尾：从最后一帧起跳，由随后的丢弃逻辑自然产出 EOF
			// （与旧行为「从头解压到尾」同结论，只是不必解压前面所有帧）。
			i = len(dir) - 1
		}
		target, newPos = dir[i].compOff, dir[i].uncompOff
	}
	if err := cf.fr.reopenAt(target); err != nil {
		// 底层流不可定位（理论上 *os.File 总可）⇒ 退回重建 reader 的旧路径。
		if _, serr := cf.f.Seek(0, io.SeekStart); serr != nil {
			return serr
		}
		fr, nerr := NewFrameReader(cf.f)
		if nerr != nil {
			return nerr
		}
		cf.fr.Close()
		cf.fr = fr
		cf.pos = 0
		return nil
	}
	cf.pos = newPos
	return nil
}

// frameDirLocked 返回帧目录（首次调用时扫描，之后缓存；调用方持锁）。
func (cf *compressedFile) frameDirLocked() ([]frameDirEntry, error) {
	if !cf.dirBuilt {
		cf.dirBuilt = true
		cf.dir, cf.dirErr = scanFrameDir(cf.f)
	}
	return cf.dir, cf.dirErr
}

// scanFrameDir 只读每帧的帧头并跳过帧体：O(帧数) 次 pread、零解压。
// 帧头与 FrameReader.readNextFrame 同一校验口径（尺寸越界即损坏）。
func scanFrameDir(ra io.ReaderAt) ([]frameDirEntry, error) {
	var (
		dir    []frameDirEntry
		off    = int64(headerSize)
		uncomp int64
		hdr    [frameHeaderSize]byte
	)
	for {
		n, err := ra.ReadAt(hdr[:], off)
		if n == 0 {
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			break // 干净的文件尾
		}
		if n < frameHeaderSize {
			return nil, fmt.Errorf("%w: truncated frame header at %d", ErrCorrupted, off)
		}
		uncompLen := int64(binary.LittleEndian.Uint32(hdr[0:4]))
		compLen := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		if uncompLen > frameSize || compLen > maxFrameSize {
			return nil, fmt.Errorf("%w: invalid frame sizes uncomp=%d comp=%d at %d", ErrCorrupted, uncompLen, compLen, off)
		}
		dir = append(dir, frameDirEntry{
			compOff: off, uncompOff: uncomp, uncompLen: uncompLen, uncompEnd: uncomp + uncompLen,
		})
		off += frameHeaderSize + compLen
		uncomp += uncompLen
	}
	return dir, nil
}

func (cf *compressedFile) Close() error {
	cf.fr.Close()
	return cf.f.Close()
}

func (s *Store) Verify(ctx context.Context, ref Ref) error {
	f, err := s.Open(ctx, ref)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, contextReader{ctx, f}, make([]byte, 64<<10))
	if err != nil {
		return err
	}
	if n != ref.Size || hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
		return ErrIntegrity
	}
	return nil
}

// Recover loads only the bounded record, then verifies RAW in a streaming
// pass. Request identity is never inferred from an untrusted stored path.
func (s *Store) Recover(ctx context.Context, id Identity) (Ref, error) {
	if err := validIdentity(id); err != nil {
		return Ref{}, err
	}
	path := filepath.Join(requestDir(id), "ref.json")
	if err := s.checkPath(path); err != nil {
		return Ref{}, err
	}
	f, err := s.root.Open(path)
	if err != nil {
		return Ref{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, 4097))
	if err != nil {
		return Ref{}, err
	}
	if len(data) > 4096 {
		return Ref{}, ErrIntegrity
	}
	var ref Ref
	if err = json.Unmarshal(data, &ref); err != nil || ref.Identity != id {
		return Ref{}, ErrIntegrity
	}
	if err = s.Verify(ctx, ref); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// Lookup is the restart-safe reference lookup; it verifies the archived file.
func (s *Store) Lookup(ctx context.Context, id Identity) (Ref, error) { return s.Recover(ctx, id) }

func (s *Store) ReadRange(ctx context.Context, ref Ref, span Span, maxBytes int64) ([]byte, error) {
	if span.Offset < 0 || span.Length < 0 || maxBytes < 0 || span.Length > maxBytes || span.Length > MaxRangeBytes || span.Offset > ref.Size || span.Length > ref.Size-span.Offset {
		return nil, ErrRange
	}
	f, err := s.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	
	// For compressed files, we need sequential read to offset
	if span.Offset > 0 {
		if _, err := io.CopyN(io.Discard, f, span.Offset); err != nil {
			return nil, err
		}
	}
	
	return io.ReadAll(contextReader{ctx, io.LimitReader(f, span.Length)})
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func validRef(ref Ref) error {
	if err := validIdentity(ref.Identity); err != nil {
		return err
	}
	b, err := hex.DecodeString(ref.SHA256)
	if err != nil || len(b) != sha256.Size || ref.Size < 0 || ref.Size > LimitBytes {
		return ErrIntegrity
	}
	return nil
}

func validIdentity(id Identity) error {
	for _, part := range []string{id.TenantID, id.SessionID, id.RequestID} {
		if len(part) == 0 || len(part) > 128 {
			return errors.New("largebody: invalid identity")
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return errors.New("largebody: invalid identity")
			}
		}
		upper := strings.ToUpper(part)
		if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" || len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
			return errors.New("largebody: reserved identity")
		}
	}
	return nil
}

func randomID() (string, error) {
	var id [16]byte
	if n, err := rand.Read(id[:]); err != nil {
		return "", err
	} else if n != len(id) {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(id[:]), nil
}

func (s *Store) syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	} // Windows does not expose directory fsync through os.File.
	f, err := s.root.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return fmt.Errorf("largebody: sync directory: %w", err)
	}
	return nil
}
