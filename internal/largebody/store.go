// Package largebody stores immutable request bodies without retaining them in
// the Go heap. References are scoped to a trusted tenant and session identity.
package largebody

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, r}, LimitBytes+1), make([]byte, 64<<10))
	if err != nil {
		return ref, err
	}
	if n > LimitBytes {
		return ref, ErrTooLarge
	}
	if err = ctx.Err(); err != nil {
		return ref, err
	}
	if err = f.Sync(); err != nil {
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

// Open does not read or hash the complete file. It validates trusted reference
// shape and size; call Verify or Recover before trusting a persisted record.
func (s *Store) Open(ctx context.Context, ref Ref) (*os.File, error) {
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
	if err != nil || !info.Mode().IsRegular() || info.Size() != ref.Size {
		_ = f.Close()
		return nil, ErrIntegrity
	}
	return f, nil
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
	return io.ReadAll(contextReader{ctx, io.NewSectionReader(f, span.Offset, span.Length)})
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
