package requestdetail

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,128}$`)

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
	payload := filePayload{Meta: meta, Bodies: bodies}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("requestdetail: marshal: %w", err)
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
		return filePayload{}, false, fmt.Errorf("requestdetail: body file exceeds %d bytes", MaxBodyFileSize)
	}
	
	// Check TTL expiration
	if s.ttl > 0 && time.Since(info.ModTime()) >= s.ttl {
		_ = os.Remove(path)
		return filePayload{}, false, nil
	}
	
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return filePayload{}, false, nil
		}
		return filePayload{}, false, err
	}
	var p filePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		// A broken local snapshot must not prevent the DB fallback.
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
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(path + ".tmp")
	return nil
}

func (s *Store) evictLocked(now time.Time) {
	for id, at := range s.updated {
		if s.ttl > 0 && now.Sub(at) >= s.ttl {
			delete(s.mem, id)
			delete(s.updated, id)
			if s.dir != "" {
				_ = os.Remove(s.filePath(id))
				_ = os.Remove(s.filePath(id) + ".tmp")
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
			_ = os.Remove(s.filePath(oldest))
			_ = os.Remove(s.filePath(oldest) + ".tmp")
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
			_ = os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
}

func (s *Store) filePath(requestID string) string {
	return filepath.Join(s.dir, requestID+".json")
}

// ValidateRequestID validates the identifier accepted by the local file store.
// It is exported so HTTP entry points can return a client error before lookup.
func ValidateRequestID(id string) error {
	if !safeRequestID.MatchString(id) {
		return fmt.Errorf("requestdetail: invalid request_id")
	}
	return nil
}

func validateRequestID(id string) error {
	return ValidateRequestID(id)
}
