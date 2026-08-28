package requestdetail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// safeRequestID accepts three families of request-id shapes observed in
// the codebase. The whitelist is intentionally narrow at the CHARACTER
// level — every accepted shape is built from [A-Za-z0-9._-] — but the
// structure constraints are strict:
//
//   - hex-only:    [0-9a-f]{32}                       (server-generated,
//                                                       case-insensitive)
//   - uuid-dashed: [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}
//   - prefixed:    starts with a letter, total 8-128 chars,
//                  no runs of dots or dashes. Examples: "req-unified-01",
//                  "req_1", "req-same-tenant", "bench-1a2b3c4d",
//                  "routing-test-<uuid>-00".
//
// 2026-08-28 (audit follow-up): the original regex
// `^[A-Za-z0-9._-]{8,128}$` was too permissive — any 8-char "abc..def"
// was accepted even though ".." could be a path-traversal vector if a
// downstream tool failed to call filepath.Base. The new pattern:
//   - exact-shape branches for hex-only and uuid-dashed (case-insensitive)
//   - prefix branch that MUST start with a letter (rejects ".hidden",
//     "..", "1abc" — anything beginning with a digit or dot)
//   - prefix branch atom = (one or more safe chars) or (one separator
//     followed by exactly one safe char). The atom trick is the
//     RE2-friendly way to forbid runs of separators since RE2 has no
//     negative-lookahead. See TestSafeRequestIDCompatibilityMatrix.
//
// Length 8-128 is enforced programmatically alongside the regex match.
// The (?i) flag covers both upper- and lowercase hex.
var safeRequestIDPattern = regexp.MustCompile(
	`(?i)^([0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[A-Za-z](?:[A-Za-z0-9]+|[._-][A-Za-z0-9])+)$`,
)

// ErrBodyTooLarge indicates that a request-detail body exceeds the storage/read limit.
var ErrBodyTooLarge = errors.New("requestdetail: body file exceeds size limit")

const (
	defaultMaxEntries = 4096
	defaultTTL        = 30 * time.Minute
	// MaxBodyFileSize is the upper limit for a single request-detail body file (10MB).
	// Files exceeding this limit are rejected to prevent OOM.
	MaxBodyFileSize = 10 * 1024 * 1024
)

// Store keeps in-flight request meta in memory and bodies on local files
// named by request_id. Redis is never used for full bodies.
type Store struct {
	dir        string
	mu         sync.RWMutex
	mem        map[string]Meta
	updated    map[string]time.Time
	maxEntries int
	ttl        time.Duration
}

type StoreOptions struct {
	MaxEntries int
	TTL        time.Duration
}

// NewStore creates a ContentStore rooted at dir. Empty dir disables file I/O
// but memory still works (useful in tests).
func NewStore(dir string) (*Store, error) {
	return NewStoreWithOptions(dir, StoreOptions{})
}

func NewStoreWithOptions(dir string, opts StoreOptions) (*Store, error) {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = defaultMaxEntries
	}
	if opts.TTL <= 0 {
		opts.TTL = defaultTTL
	}
	s := &Store{dir: dir, mem: make(map[string]Meta), updated: make(map[string]time.Time), maxEntries: opts.MaxEntries, ttl: opts.TTL}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("requestdetail: mkdir %s: %w", dir, err)
	}
	s.cleanupFiles(time.Now())
	return s, nil
}

// Dir returns the configured content directory (may be empty).
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// PutMeta stores/replaces lightweight meta in memory.
func (s *Store) PutMeta(meta Meta) error {
	if s == nil {
		return errors.New("requestdetail: nil store")
	}
	if err := validateRequestID(meta.RequestID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem[meta.RequestID] = meta
	s.updated[meta.RequestID] = time.Now()
	s.evictLocked(time.Now())
	return nil
}

// Put stores meta in memory and optionally writes bodies to a local file.
// When bodies is nil, only memory meta is updated.
func (s *Store) Put(meta Meta, bodies *Bodies) error {
	if bodies == nil {
		return s.PutMeta(meta)
	}
	return s.PutBodies(meta, *bodies)
}

// PutBodies writes bodies to {dir}/{request_id}.json and refreshes meta.
func (s *Store) PutBodies(meta Meta, bodies Bodies) error {
	if s == nil {
		return errors.New("requestdetail: nil store")
	}
	if err := validateRequestID(meta.RequestID); err != nil {
		return err
	}
	if bodyBytes := len(bodies.RequestBody) + len(bodies.ResponseBody) + len(bodies.OutboundBody); bodyBytes > MaxBodyFileSize {
		return fmt.Errorf("%w: %d bytes (limit %d)", ErrBodyTooLarge, bodyBytes, MaxBodyFileSize)
	}
	payload := filePayload{Meta: meta, Bodies: bodies}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("requestdetail: marshal: %w", err)
	}
	if int64(len(raw)) > MaxBodyFileSize {
		return fmt.Errorf("%w: %d bytes (limit %d)", ErrBodyTooLarge, len(raw), MaxBodyFileSize)
	}

	// Hold the lifecycle lock through file replacement and memory publication.
	// This prevents Clear or another Put for the same request from observing a
	// half-published state. The lock is intentionally process-wide: request
	// detail writes are small and correctness is more important than parallel
	// file I/O here.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" {
		path := s.filePath(meta.RequestID)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o640); err != nil {
			return fmt.Errorf("requestdetail: write tmp: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("requestdetail: rename: %w", err)
		}
	}
	s.mem[meta.RequestID] = meta
	s.updated[meta.RequestID] = time.Now()
	s.evictLocked(time.Now())
	return nil
}

// GetMeta returns in-memory meta when present.
func (s *Store) GetMeta(requestID string) (Meta, bool) {
	if s == nil {
		return Meta{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(time.Now())
	m, ok := s.mem[requestID]
	return m, ok
}

// GetFile loads bodies from the local file when present.
func (s *Store) GetFile(requestID string) (filePayload, bool, error) {
	if s == nil || s.dir == "" {
		return filePayload{}, false, nil
	}
	if err := validateRequestID(requestID); err != nil {
		return filePayload{}, false, err
	}

	// Hold lock only for eviction and path resolution (fast operations)
	s.mu.Lock()
	s.evictLocked(time.Now())
	path := s.filePath(requestID)
	s.mu.Unlock()

	// File I/O operations outside lock to avoid blocking concurrent reads
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return filePayload{}, false, nil
		}
		return filePayload{}, false, err
	}

	// Check file size before reading to prevent OOM on oversized bodies
	if info.Size() > MaxBodyFileSize {
		return filePayload{}, false, fmt.Errorf("%w: %d bytes (limit %d)", ErrBodyTooLarge, info.Size(), MaxBodyFileSize)
	}

	// Check TTL expiration. Re-check and remove under the lifecycle lock so a
	// concurrent atomic Put cannot be followed by deletion of its fresh file.
	if s.ttl > 0 && time.Since(info.ModTime()) >= s.ttl {
		s.mu.Lock()
		defer s.mu.Unlock()
		latest, statErr := os.Stat(path)
		if statErr == nil && time.Since(latest.ModTime()) >= s.ttl {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				storeEvictionFailuresTotal.WithLabelValues("ttl", normalizeRemoveErr(removeErr)).Inc()
				slog.Warn("requestdetail: ttl remove on read failed",
					"request_id", requestID,
					"path", path,
					"error", removeErr)
				return filePayload{}, false, removeErr
			}
		}
		if os.IsNotExist(statErr) {
			return filePayload{}, false, nil
		}
		if statErr != nil {
			return filePayload{}, false, statErr
		}
		return filePayload{}, false, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return filePayload{}, false, nil
		}
		return filePayload{}, false, err
	}
	defer f.Close()

	// Re-stat the opened descriptor and cap the read to the same file handle.
	// This closes the stat/read TOCTOU window introduced by lock-free file I/O.
	info, err = f.Stat()
	if err != nil {
		return filePayload{}, false, err
	}
	if info.Size() > MaxBodyFileSize {
		return filePayload{}, false, fmt.Errorf("%w: %d bytes (limit %d)", ErrBodyTooLarge, info.Size(), MaxBodyFileSize)
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxBodyFileSize+1))
	if err != nil {
		return filePayload{}, false, err
	}
	if int64(len(raw)) > MaxBodyFileSize {
		return filePayload{}, false, fmt.Errorf("%w: %d bytes (limit %d)", ErrBodyTooLarge, info.Size(), MaxBodyFileSize)
	}
	var p filePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		// A broken local snapshot must not prevent the DB fallback.
		storeMalformedSnapshotTotal.Inc()
		slog.Warn("requestdetail: ignoring malformed local snapshot", "request_id", requestID, "error", err)
		return filePayload{}, false, nil
	}
	if p.Meta.RequestID == "" {
		p.Meta.RequestID = requestID
	} else if p.Meta.RequestID != requestID {
		// Never return a payload whose identity disagrees with its filename.
		slog.Warn("requestdetail: ignoring mismatched local snapshot", "request_id", requestID, "payload_request_id", p.Meta.RequestID)
		return filePayload{}, false, nil
	}
	return p, true, nil
}

// HasLocal reports whether memory or file still holds this request.
func (s *Store) HasLocal(requestID string) bool {
	if s == nil {
		return false
	}
	if _, ok := s.GetMeta(requestID); ok {
		return true
	}
	_, ok, _ := s.GetFile(requestID)
	return ok
}

// Clear removes memory meta and the local file after DB persist.
// Surface os.Remove failures via slog.Warn + the residue counters so
// dashboards can alert on persistent failures (read-only mount, NFS
// stale handle, chmod 0). 2026-08-28 (audit follow-up): previously the
// failure path only emitted a single warn line; operators had no
// quantitative signal for sensitive-file residue.
//
// 2026-08-29 (audit follow-up): the previous implementation returned
// immediately on the primary file's Remove failure, leaving the .tmp
// sidecar behind. Both files must be attempted in a single pass —
// residue is residue regardless of which file it lives in. The errors
// are joined so the caller still sees a non-nil return.
func (s *Store) Clear(requestID string) error {
	if s == nil {
		return nil
	}
	if validateRequestID(requestID) != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mem, requestID)
	delete(s.updated, requestID)
	if s.dir == "" {
		return nil
	}
	path := s.filePath(requestID)
	var errs []error

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		storeClearFailuresTotal.WithLabelValues(normalizeRemoveErr(err)).Inc()
		slog.Warn("requestdetail: clear after persist failed to remove file",
			"request_id", requestID,
			"path", path,
			"error", err)
		errs = append(errs, fmt.Errorf("primary: %w", err))
	}
	if tmpErr := os.Remove(path + ".tmp"); tmpErr != nil && !os.IsNotExist(tmpErr) {
		storeClearFailuresTotal.WithLabelValues(normalizeRemoveErr(tmpErr)).Inc()
		slog.Warn("requestdetail: clear after persist failed to remove tmp sidecar",
			"request_id", requestID,
			"path", path+".tmp",
			"error", tmpErr)
		errs = append(errs, fmt.Errorf("tmp: %w", tmpErr))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	storeClearSuccessTotal.Inc()
	return nil
}

func (s *Store) evictLocked(now time.Time) {
	for id, at := range s.updated {
		if s.ttl > 0 && now.Sub(at) >= s.ttl {
			delete(s.mem, id)
			delete(s.updated, id)
			if s.dir != "" {
				if err := os.Remove(s.filePath(id)); err != nil && !os.IsNotExist(err) {
					storeEvictionFailuresTotal.WithLabelValues("ttl", normalizeRemoveErr(err)).Inc()
					slog.Warn("requestdetail: ttl eviction remove failed",
						"request_id", id,
						"error", err)
				}
				if err := os.Remove(s.filePath(id) + ".tmp"); err != nil && !os.IsNotExist(err) {
					storeEvictionFailuresTotal.WithLabelValues("ttl", normalizeRemoveErr(err)).Inc()
					slog.Warn("requestdetail: ttl eviction tmp remove failed",
						"request_id", id,
						"error", err)
				}
			}
		}
	}
	for len(s.mem) > s.maxEntries {
		var oldest string
		var oldestAt time.Time
		for id, at := range s.updated {
			if oldest == "" || at.Before(oldestAt) {
				oldest, oldestAt = id, at
			}
		}
		if oldest == "" {
			break
		}
		delete(s.mem, oldest)
		delete(s.updated, oldest)
		if s.dir != "" {
			if err := os.Remove(s.filePath(oldest)); err != nil && !os.IsNotExist(err) {
				storeEvictionFailuresTotal.WithLabelValues("lru", normalizeRemoveErr(err)).Inc()
				slog.Warn("requestdetail: lru eviction remove failed",
					"request_id", oldest,
					"error", err)
			}
			if err := os.Remove(s.filePath(oldest) + ".tmp"); err != nil && !os.IsNotExist(err) {
				storeEvictionFailuresTotal.WithLabelValues("lru", normalizeRemoveErr(err)).Inc()
				slog.Warn("requestdetail: lru eviction tmp remove failed",
					"request_id", oldest,
					"error", err)
			}
		}
	}
}

func (s *Store) cleanupFiles(now time.Time) {
	if s.dir == "" || s.ttl <= 0 {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) >= s.ttl {
			if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				storeEvictionFailuresTotal.WithLabelValues("cleanup", normalizeRemoveErr(err)).Inc()
				slog.Warn("requestdetail: cleanup remove failed",
					"path", entry.Name(),
					"error", err)
			}
		}
	}
}

func (s *Store) filePath(requestID string) string {
	return filepath.Join(s.dir, requestID+".json")
}

// ValidateRequestID validates the identifier accepted by the local file store.
// It is exported so HTTP entry points can return a client error before lookup.
//
// The check has three parts:
//   - structural: safeRequestIDPattern matches one of the three accepted
//     shapes (hex-only, uuid-dashed, prefixed).
//   - length: 8 <= len(id) <= 128 (enforced separately because RE2's
//     {min,max} cannot distinguish "8 chars of safe" from "8 chars including
//     separators").
//   - non-empty: empty string is rejected by both above checks, but listed
//     here for documentation.
func ValidateRequestID(id string) error {
	if id == "" {
		return fmt.Errorf("requestdetail: invalid request_id")
	}
	if len(id) < 8 || len(id) > 128 {
		return fmt.Errorf("requestdetail: invalid request_id")
	}
	if !safeRequestIDPattern.MatchString(id) {
		return fmt.Errorf("requestdetail: invalid request_id")
	}
	return nil
}

func validateRequestID(id string) error {
	return ValidateRequestID(id)
}
