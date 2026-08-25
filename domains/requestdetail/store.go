package requestdetail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,128}$`)

// Store keeps in-flight request meta in memory and bodies on local files
// named by request_id. Redis is never used for full bodies.
type Store struct {
	dir string
	mu  sync.RWMutex
	mem map[string]Meta
}

// NewStore creates a ContentStore rooted at dir. Empty dir disables file I/O
// but memory still works (useful in tests).
func NewStore(dir string) (*Store, error) {
	s := &Store{dir: dir, mem: make(map[string]Meta)}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("requestdetail: mkdir %s: %w", dir, err)
	}
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
	s.mem[meta.RequestID] = meta
	s.mu.Unlock()
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
	s.mu.Lock()
	s.mem[meta.RequestID] = meta
	s.mu.Unlock()
	if s.dir == "" {
		return nil
	}
	payload := filePayload{Meta: meta, Bodies: bodies}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("requestdetail: marshal: %w", err)
	}
	path := s.filePath(meta.RequestID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return fmt.Errorf("requestdetail: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("requestdetail: rename: %w", err)
	}
	return nil
}

// GetMeta returns in-memory meta when present.
func (s *Store) GetMeta(requestID string) (Meta, bool) {
	if s == nil {
		return Meta{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	raw, err := os.ReadFile(s.filePath(requestID))
	if err != nil {
		if os.IsNotExist(err) {
			return filePayload{}, false, nil
		}
		return filePayload{}, false, err
	}
	var p filePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return filePayload{}, false, err
	}
	if p.Meta.RequestID == "" {
		p.Meta.RequestID = requestID
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
	s.mu.Lock()
	delete(s.mem, requestID)
	s.mu.Unlock()
	if s.dir == "" || validateRequestID(requestID) != nil {
		return nil
	}
	path := s.filePath(requestID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(path + ".tmp")
	return nil
}

func (s *Store) filePath(requestID string) string {
	return filepath.Join(s.dir, requestID+".json")
}

func validateRequestID(id string) error {
	if !safeRequestID.MatchString(id) {
		return fmt.Errorf("requestdetail: invalid request_id")
	}
	return nil
}
