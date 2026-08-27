// Package fsstore implements the filesystem-backed persistence
// layer used as a fallback (or final tier) when PostgreSQL or Redis
// is unavailable. It is intentionally a small, well-bounded surface
// area so it can be swapped in via dependency injection without
// reshaping every PG caller.
//
// Phase B (2026-08-26) — directory layout
// ─────────────────────────────────────────
//
//	${LLM_GATEWAY_FS_ROOT}/
//	├── index/
//	│   ├── entities.bleve/          # provider/tenant/user/key metadata
//	│   └── requests.bleve/          # request records (request_id 倒排)
//	├── entities/
//	│   ├── providers/<provider_id>.json
//	│   ├── tenants/<tenant_id>.json
//	│   ├── users/<user_id>.json
//	│   └── api_keys/<key_id>.json
//	├── requests/
//	│   └── YYYY/MM/DD/<request_id>.json
//	└── bodies/
//	    └── YYYY/MM/DD/<request_id>/
//	        ├── request.jsonl.gz
//	        └── response.jsonl.gz
//
// Concurrency
// ───────────
//   - Entity writes use copy-on-write + atomic rename so concurrent
//     writers never see a half-written JSON file.
//   - Cross-process writers serialize on a syscall.Flock held on the
//     final destination path (POSIX; Windows ignores).
//   - Request bodies live under bodies/YYYY/MM/DD/<request_id>/ as
//     gzip-compressed JSON-lines; the parent metadata file under
//     requests/ points to them so a search hit can stream the body
//     lazily.
//   - The Bleve indices let /admin search serve hits without walking
//     the filesystem tree.
//
// Failure semantics
// ─────────────────
// Every public method returns an error rather than panicking; the
// caller (cmd/gateway/main.go) decides whether PG, FS, or both are
// authoritative at startup. We do not silently swallow errors
// because losing a request record should be loud.
package fsstore

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
)

// Config configures a Store. Zero value is not usable; call New
// with sensible defaults.
type Config struct {
	// Root is the absolute filesystem root. Required.
	Root string
	// ReadOnly disables writes (useful for static-replay tooling).
	ReadOnly bool
}

// Entity kind constants — keep aligned with the directory layout.
const (
	KindProvider = "providers"
	KindTenant   = "tenants"
	KindUser     = "users"
	KindAPIKey   = "api_keys"
)

// Store is the package handle; safe for concurrent use.
type Store struct {
	cfg     Config
	entsIdx bleve.Index // entities metadata index
	reqIdx  bleve.Index // request records index

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-key coarse mutex for in-process writes
}

// New opens (or creates) the store rooted at cfg.Root. Returns a
// non-nil error if either Bleve index cannot be opened — callers
// should treat that as "FS store unavailable" and fall back to PG.
func New(cfg Config) (*Store, error) {
	if cfg.Root == "" {
		return nil, errors.New("fsstore: empty Root")
	}
	if err := os.MkdirAll(filepath.Join(cfg.Root, "index", "entities.bleve"), 0o755); err != nil {
		return nil, fmt.Errorf("fsstore: mkdir entities index: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Root, "index", "requests.bleve"), 0o755); err != nil {
		return nil, fmt.Errorf("fsstore: mkdir requests index: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Root, "entities", KindProvider), 0o755); err != nil {
		return nil, fmt.Errorf("fsstore: mkdir providers: %w", err)
	}
	for _, k := range []string{KindTenant, KindUser, KindAPIKey} {
		if err := os.MkdirAll(filepath.Join(cfg.Root, "entities", k), 0o755); err != nil {
			return nil, fmt.Errorf("fsstore: mkdir %s: %w", k, err)
		}
	}

	openOrNew := func(dir string, im mapping.IndexMapping) (bleve.Index, error) {
		idx, err := bleve.Open(dir)
		if err == nil {
			return idx, nil
		}
		if im == nil {
			im = bleve.NewIndexMapping()
		}
		return bleve.New(dir, im)
	}
	entsIdx, err := openOrNew(filepath.Join(cfg.Root, "index", "entities.bleve"), nil)
	if err != nil {
		return nil, fmt.Errorf("fsstore: open entities index: %w", err)
	}
	// Requests index: the "id" field must be keyword-analyzed so
	// GetRequest's TermQuery(id) matches exactly (hyphenated ids would
	// otherwise tokenize, and the previous fieldless query targeted the
	// nonexistent _all field — every GetRequest returned ErrNotExist).
	reqIdx, err := openOrNew(filepath.Join(cfg.Root, "index", "requests.bleve"), newRequestIndexMapping())
	if err != nil {
		_ = entsIdx.Close()
		return nil, fmt.Errorf("fsstore: open requests index: %w", err)
	}

	return &Store{
		cfg:     cfg,
		entsIdx: entsIdx,
		reqIdx:  reqIdx,
		locks:   make(map[string]*sync.Mutex),
	}, nil
}

// Close flushes + closes the Bleve indices. Safe to call multiple
// times; safe to call on a nil-receiver.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	if s.entsIdx != nil {
		if err := s.entsIdx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.entsIdx = nil
	}
	if s.reqIdx != nil {
		if err := s.reqIdx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.reqIdx = nil
	}
	return firstErr
}

// Entity describes a single metadata record (provider / tenant / user
// / api_key). Payload is the JSON-serializable body the caller
// cares about; we keep it untyped so the caller can store its own
// shape without coupling to fsstore.
type Entity struct {
	ID        string                 `json:"id"`
	Kind      string                 `json:"kind"`
	UpdatedAt time.Time              `json:"updated_at"`
	Payload   map[string]interface{} `json:"payload"`
}

// PutEntity writes the entity to entities/<kind>/<id>.json using
// copy-on-write + atomic rename under flock. The Bleve metadata
// index is updated on the same goroutine so the next search sees
// the new doc.
func (s *Store) PutEntity(e Entity) error {
	if s == nil {
		return errors.New("fsstore: nil store")
	}
	if e.ID == "" {
		return errors.New("fsstore: empty entity id")
	}
	if !validKind(e.Kind) {
		return fmt.Errorf("fsstore: invalid kind %q", e.Kind)
	}
	if s.cfg.ReadOnly {
		return errors.New("fsstore: read-only")
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now().UTC()
	}

	rel := filepath.Join("entities", e.Kind, e.ID+".json")
	final := filepath.Join(s.cfg.Root, rel)
	tmp := final + ".tmp." + fmt.Sprintf("%d", os.Getpid())

	body, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("fsstore: marshal entity: %w", err)
	}

	s.keyLock(rel).Lock()
	defer s.keyLock(rel).Unlock()

	// Cross-process lock on the FINAL path so two gateway instances
	// writing the same entity do not race. POSIX only.
	if err := flockFile(final, tmp, body); err != nil {
		return err
	}

	// Best-effort index update. If this fails the file is still the
	// source of truth — we will rebuild the index from disk during
	// RebuildIndexes.
	if err := s.entsIdx.Index(e.ID, entityDoc(e)); err != nil {
		return fmt.Errorf("fsstore: index entity: %w", err)
	}
	return nil
}

// GetEntity loads the entity from disk. Returns os.ErrNotExist if
// the file is missing.
func (s *Store) GetEntity(kind, id string) (*Entity, error) {
	if s == nil {
		return nil, errors.New("fsstore: nil store")
	}
	if !validKind(kind) {
		return nil, fmt.Errorf("fsstore: invalid kind %q", kind)
	}
	path := filepath.Join(s.cfg.Root, "entities", kind, id+".json")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var e Entity
	if err := json.NewDecoder(f).Decode(&e); err != nil {
		return nil, fmt.Errorf("fsstore: decode entity: %w", err)
	}
	return &e, nil
}

// ListEntities returns the entity IDs of the given kind in
// lexicographic order. Cheap walk; suitable for small sets or admin
// tooling — not for hot-path reads.
func (s *Store) ListEntities(kind string) ([]string, error) {
	if s == nil {
		return nil, errors.New("fsstore: nil store")
	}
	if !validKind(kind) {
		return nil, fmt.Errorf("fsstore: invalid kind %q", kind)
	}
	dir := filepath.Join(s.cfg.Root, "entities", kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

// RequestRecord is the metadata side of a single request lifecycle.
// Bodies live in bodies/YYYY/MM/DD/<request_id>/{request,response}.jsonl.gz.
type RequestRecord struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	TenantID  string    `json:"tenant_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	Status    string    `json:"status,omitempty"`
	CostUSD   float64   `json:"cost_usd,omitempty"`
	// RequestClass is the V6-W1.6 R8 request class (immediate|scheduled,
	// migration 610 parity with request_logs_hot.request_class). Empty means
	// immediate (matching the DB column default).
	RequestClass string `json:"request_class,omitempty"`
	// DueAt is the scheduled execution time; nil for immediate requests.
	DueAt *time.Time             `json:"due_at,omitempty"`
	Attrs map[string]interface{} `json:"attrs,omitempty"`
}

// PutRequest persists the request record. It assumes the body
// fragments have already been written via PutBodyPart; the
// record itself is the index pointer.
func (s *Store) PutRequest(r RequestRecord) error {
	if s == nil {
		return errors.New("fsstore: nil store")
	}
	if r.ID == "" {
		return errors.New("fsstore: empty request id")
	}
	if s.cfg.ReadOnly {
		return errors.New("fsstore: read-only")
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}

	dir := filepath.Join(s.cfg.Root, "requests", datePath(r.StartedAt))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fsstore: mkdir request dir: %w", err)
	}
	final := filepath.Join(dir, r.ID+".json")
	tmp := final + ".tmp." + fmt.Sprintf("%d", os.Getpid())

	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("fsstore: marshal request: %w", err)
	}

	rel := filepath.Join("requests", datePath(r.StartedAt), r.ID+".json")
	s.keyLock(rel).Lock()
	defer s.keyLock(rel).Unlock()
	if err := flockFile(final, tmp, body); err != nil {
		return err
	}

	if err := s.reqIdx.Index(r.ID, requestDoc(r)); err != nil {
		return fmt.Errorf("fsstore: index request: %w", err)
	}
	return nil
}

// GetRequest loads a request record by id; the caller must know the
// approximate started_at day for the lookup to be efficient (we use
// a Bleve search to find the right date shard).
func (s *Store) GetRequest(id string) (*RequestRecord, error) {
	if s == nil {
		return nil, errors.New("fsstore: nil store")
	}
	if id == "" {
		return nil, errors.New("fsstore: empty id")
	}

	// Query by document ID (_id): a bare TermQuery depends on the index
	// mapping and can miss hyphenated request IDs. DocIDQuery is the
	// exact-match lookup path.
	req := bleve.NewSearchRequest(bleve.NewDocIDQuery([]string{id}))
	req.Fields = []string{"shard_date", "started_at"}
	res, err := s.reqIdx.Search(req)
	if err != nil {
		return nil, fmt.Errorf("fsstore: search request id: %w", err)
	}
	if res.Total == 0 {
		return nil, os.ErrNotExist
	}
	// Multiple hits can only mean a duplicate id (same id reused).
	// We pick the newest by Sort if available, otherwise the first.
	hit := res.Hits[0]
	dateStr := ""
	if v, ok := hit.Fields["shard_date"].(string); ok && len(v) == 10 {
		dateStr = v // YYYY/MM/DD — matches datePath layout (keyword, timezone-stable)
	}
	if dateStr == "" {
		return nil, fmt.Errorf("fsstore: hit %s missing shard_date", hit.ID)
	}
	// datePath uses "YYYY/MM/DD" but the indexed started_at comes back as
	// RFC3339 ("YYYY-MM-DD..."), so convert to the on-disk shard format.
	dateDir := dateStr
	if len(dateDir) == 10 {
		dateDir = dateDir[:4] + "/" + dateDir[5:7] + "/" + dateDir[8:10]
	}

	path := filepath.Join(s.cfg.Root, "requests", dateDir, id+".json")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rr RequestRecord
	if err := json.NewDecoder(f).Decode(&rr); err != nil {
		return nil, fmt.Errorf("fsstore: decode request: %w", err)
	}
	return &rr, nil
}

// ListRequestsInDay walks the day directory and returns all record
// IDs in the given date (YYYY-MM-DD). Cheap and Bleve-free.
func (s *Store) ListRequestsInDay(date string) ([]string, error) {
	if s == nil {
		return nil, errors.New("fsstore: nil store")
	}
	// On-disk shards use "YYYY/MM/DD"; callers pass "YYYY-MM-DD".
	dateDir := date
	if len(dateDir) == 10 && strings.Count(dateDir, "-") == 2 {
		dateDir = dateDir[:4] + "/" + dateDir[5:7] + "/" + dateDir[8:10]
	}
	dir := filepath.Join(s.cfg.Root, "requests", dateDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

// BodyPart describes one half (request or response) of a request.
type BodyPart string

const (
	BodyRequest  BodyPart = "request"
	BodyResponse BodyPart = "response"
)

// PutBodyPart appends a JSON-lines fragment for the request body.
// Multiple calls per request are supported (e.g. streaming chunks)
// — each call appends one JSON line.
func (s *Store) PutBodyPart(requestID string, ts time.Time, part BodyPart, payload []byte) error {
	if s == nil {
		return errors.New("fsstore: nil store")
	}
	if requestID == "" {
		return errors.New("fsstore: empty request id")
	}
	if s.cfg.ReadOnly {
		return errors.New("fsstore: read-only")
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	dir := filepath.Join(s.cfg.Root, "bodies", datePath(ts), requestID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fsstore: mkdir body dir: %w", err)
	}
	fname := string(part) + ".jsonl.gz"
	final := filepath.Join(dir, fname)
	rel := filepath.Join("bodies", datePath(ts), requestID, fname)

	s.keyLock(rel).Lock()
	defer s.keyLock(rel).Unlock()

	// Append to the gzip stream. We open in read-write-create mode
	// and on first write emit a fresh gzip header; on subsequent
	// writes we re-read the existing stream and re-emit a fresh
	// gzip file (simpler than incremental gzip, and these bodies are
	// short-lived — typically tens of KB).
	if err := appendGzippedLine(final, payload); err != nil {
		return err
	}
	return nil
}

// ReadBodyPart streams the body part back as JSON lines.
func (s *Store) ReadBodyPart(requestID string, ts time.Time, part BodyPart) ([][]byte, error) {
	if s == nil {
		return nil, errors.New("fsstore: nil store")
	}
	dir := filepath.Join(s.cfg.Root, "bodies", datePath(ts), requestID)
	fname := string(part) + ".jsonl.gz"
	path := filepath.Join(dir, fname)
	return readGzippedLines(path)
}

// RebuildIndexes walks the filesystem and rebuilds the Bleve
// indices. Intended to run on startup after a crash or after the
// admin manually triggers a re-index.
func (s *Store) RebuildIndexes() error {
	if s == nil {
		return errors.New("fsstore: nil store")
	}

	// Entities
	for _, kind := range []string{KindProvider, KindTenant, KindUser, KindAPIKey} {
		dir := filepath.Join(s.cfg.Root, "entities", kind)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("fsstore: rebuild %s: %w", kind, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			var ent Entity
			if err := json.NewDecoder(f).Decode(&ent); err != nil {
				f.Close()
				return fmt.Errorf("fsstore: rebuild decode %s: %w", path, err)
			}
			f.Close()
			if err := s.entsIdx.Index(ent.ID, entityDoc(ent)); err != nil {
				return err
			}
		}
	}

	// Requests: walk each YYYY/MM/DD directory.
	requestsRoot := filepath.Join(s.cfg.Root, "requests")
	dayDirs, err := os.ReadDir(requestsRoot)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsstore: rebuild requests: %w", err)
	}
	for _, year := range dayDirs {
		if !year.IsDir() {
			continue
		}
		yearPath := filepath.Join(requestsRoot, year.Name())
		months, err := os.ReadDir(yearPath)
		if err != nil {
			return err
		}
		for _, month := range months {
			if !month.IsDir() {
				continue
			}
			monthPath := filepath.Join(yearPath, month.Name())
			days, err := os.ReadDir(monthPath)
			if err != nil {
				return err
			}
			for _, day := range days {
				if !day.IsDir() {
					continue
				}
				dayPath := filepath.Join(monthPath, day.Name())
				files, err := os.ReadDir(dayPath)
				if err != nil {
					return err
				}
				for _, f := range files {
					if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
						continue
					}
					path := filepath.Join(dayPath, f.Name())
					rf, err := os.Open(path)
					if err != nil {
						return err
					}
					var rr RequestRecord
					if err := json.NewDecoder(rf).Decode(&rr); err != nil {
						rf.Close()
						return fmt.Errorf("fsstore: rebuild decode %s: %w", path, err)
					}
					rf.Close()
					if err := s.reqIdx.Index(rr.ID, requestDoc(rr)); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

// ───────────── helpers ─────────────

func validKind(k string) bool {
	switch k {
	case KindProvider, KindTenant, KindUser, KindAPIKey:
		return true
	}
	return false
}

// datePath renders t as YYYY/MM/DD for directory sharding.
func datePath(t time.Time) string {
	return t.UTC().Format("2006/01/02")
}

// flockFile holds an exclusive flock on `final` while writing
// `body` to `tmp` then atomically renaming tmp → final. POSIX only
// (Windows ignores LockFileEx fallback at this layer — the
// cross-process guarantee is best-effort on Win32).
func flockFile(final, tmp string, body []byte) error {
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fsstore: mkdir %s: %w", dir, err)
	}

	// Open the final file first (creating if absent) so we can
	// flock on a stable path. The flock is released by close.
	lock, err := os.OpenFile(final, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("fsstore: open lock file: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("fsstore: flock: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("fsstore: write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("fsstore: rename: %w", err)
	}
	return nil
}

func (s *Store) keyLock(rel string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.locks[rel]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.locks[rel] = m
	return m
}

func entityDoc(e Entity) map[string]interface{} {
	doc := map[string]interface{}{
		"id":         e.ID,
		"kind":       e.Kind,
		"updated_at": e.UpdatedAt,
	}
	for k, v := range e.Payload {
		doc[k] = v
	}
	return doc
}

func requestDoc(r RequestRecord) map[string]interface{} {
	return map[string]interface{}{
		"id":         r.ID,
		"started_at": r.StartedAt,
		"ended_at":   r.EndedAt,
		"tenant_id":  r.TenantID,
		"user_id":    r.UserID,
		"provider":   r.Provider,
		"model":      r.Model,
		"status":     r.Status,
		"cost_usd":   r.CostUSD,
		// V6-W1.6 R8: searchable request class; empty indexes as immediate.
		"request_class": requestClassOrDefault(r.RequestClass),
		// shard_date mirrors datePath(r.StartedAt) so GetRequest resolves the
		// day directory deterministically (deriving it from bleve's serialized
		// started_at drifted across timezones).
		"shard_date": r.StartedAt.UTC().Format("2006/01/02"),
	}
}

// appendGzippedLine reads any existing gzip stream, decomposes
// each JSON line, appends `line`, and re-writes the stream. We
// trade efficiency for code simplicity — bodies are typically tens
// to hundreds of KB total so the cost is negligible.
func appendGzippedLine(path string, line []byte) error {
	existing, err := readGzippedLines(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsstore: read body: %w", err)
	}
	existing = append(existing, line)

	tmp := path + ".tmp." + fmt.Sprintf("%d", os.Getpid())
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("fsstore: create body tmp: %w", err)
	}
	gz := gzip.NewWriter(out)
	enc := json.NewEncoder(gz)
	for _, l := range existing {
		if err := enc.Encode(json.RawMessage(l)); err != nil {
			gz.Close()
			out.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := gz.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func readGzippedLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("fsstore: gzip reader: %w", err)
	}
	defer gz.Close()
	var out [][]byte
	dec := json.NewDecoder(gz)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// requestClassOrDefault normalizes the FS record class for indexing/writes:
// the DB column is NOT NULL DEFAULT 'immediate', so the FS tier mirrors the
// same default instead of storing empty strings.
func requestClassOrDefault(c string) string {
	if c == "" {
		return "immediate"
	}
	return c
}

// newRequestIndexMapping builds the requests-index mapping: dynamic fields
// plus an exact-match keyword "id" (GetRequest lookup key).
func newRequestIndexMapping() mapping.IndexMapping {
	im := bleve.NewIndexMapping()
	doc := mapping.NewDocumentMapping() // dynamic by default; adds the keyword id field
	idField := bleve.NewTextFieldMapping()
	idField.Analyzer = "keyword"
	doc.AddFieldMappingsAt("id", idField)
	im.DefaultMapping = doc
	return im
}
