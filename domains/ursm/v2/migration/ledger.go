package migration

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
)

// Classification values for the preflight pass (doc 14 §4 + 15 §2).
type Classification string

const (
	ClassificationMigratable               Classification = "migratable"
	ClassificationCanonicalPresent         Classification = "canonical_present"
	ClassificationAmbiguous                Classification = "ambiguous"
	ClassificationExcludedNonAuthoritative Classification = "excluded_non_authoritative"
	ClassificationConflict                 Classification = "conflict"
)

// ItemStatus tracks the per-key state machine (doc 15 §4). Both Item
// (on-disk) and Entry (preflight in-memory) share the same enum.
type ItemStatus string

const (
	StatusDiscovered ItemStatus = "discovered"
	StatusClassified ItemStatus = "classified"
	StatusCopied     ItemStatus = "copied"
	StatusVerified   ItemStatus = "verified"
	StatusObserved   ItemStatus = "observed"
	StatusCleaned    ItemStatus = "cleaned"
	StatusExcluded   ItemStatus = "excluded"
	StatusAmbiguous  ItemStatus = "ambiguous"
	StatusConflict   ItemStatus = "conflict"
	StatusRolledBack ItemStatus = "rolled_back"
)

// EntryStatus is an alias for ItemStatus kept so preflight.go (which names
// the type EntryStatus in its struct field) can refer to the same constants
// without an extra layer of indirection.
type EntryStatus = ItemStatus

// Legacy aliases preserved for callers that import the short form.
const (
	ClassMigratable               = ClassificationMigratable
	ClassCanonicalPresent         = ClassificationCanonicalPresent
	ClassAmbiguous                = ClassificationAmbiguous
	ClassExcludedNonAuthoritative = ClassificationExcludedNonAuthoritative
	ClassConflict                 = ClassificationConflict
)

// Item is a single source-key entry in the migration ledger. Fields are
// stable for the lifetime of a migration run; checksum + pttl_ms are
// snapshotted at classification time so resume/copy are deterministic.
type Item struct {
	SourceKey        string        `json:"source_key"`
	KeyType          string        `json:"key_type,omitempty"`
	CanonicalKey     string        `json:"canonical_key,omitempty"`
	SchemaSource     string        `json:"schema_source,omitempty"`
	Classification   Classification `json:"classification"`
	Reason           string        `json:"reason,omitempty"`
	PTTLMs           int64         `json:"pttl_ms,omitempty"`
	FieldChecksum    string        `json:"field_checksum,omitempty"`
	Generation       int64         `json:"generation,omitempty"`
	ScanRunID        string        `json:"scan_run_id,omitempty"`
	Status           ItemStatus    `json:"status"`
	CopiedAtUnixMs   int64         `json:"copied_at_unix_ms,omitempty"`
	CleanedAtUnixMs  int64         `json:"cleaned_at_unix_ms,omitempty"`
}

// Ledger is an append-only NDJSON stream on disk. The on-disk file is the
// authoritative resume / audit artefact; nothing else needs to be persisted
// in lockstep with it. All write methods serialize to make concurrent
// appends safe within a process.
type Ledger struct {
	mu   sync.Mutex
	path string
}

// NewLedger returns a handle to the NDJSON file at path. The file is opened
// lazily on first Append. Existence is not required up front so callers can
// bootstrap a fresh run by passing a path under t.TempDir().
func NewLedger(path string) *Ledger {
	return &Ledger{path: path}
}

// Path returns the absolute path used by the ledger.
func (l *Ledger) Path() string { return l.path }

// Append writes one item, flushed to disk, in JSON newline-delimited form.
func (l *Ledger) Append(item Item) error {
	if item.SourceKey == "" {
		return fmt.Errorf("migration: ledger item requires source_key")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("migration: open ledger: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(item); err != nil {
		return fmt.Errorf("migration: encode ledger item: %w", err)
	}
	return nil
}

// LoadAll reads the entire ledger file. Missing file is not an error; the
// returned slice is empty. The order matches on-disk insertion order.
func (l *Ledger) LoadAll() ([]Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("migration: open ledger: %w", err)
	}
	defer f.Close()
	var items []Item
	dec := json.NewDecoder(f)
	for {
		var it Item
		if err := dec.Decode(&it); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("migration: decode ledger: %w", err)
		}
		items = append(items, it)
	}
	return items, nil
}

// ExistingItem returns the most recent item for sourceKey if any. A resumed
// run reuses it as the "already classified" basis; if classification agrees
// and the field checksum is unchanged the work can be skipped (copy/cleanup
// idempotency, doc 14 §6.1 last paragraph).
func (l *Ledger) ExistingItem(sourceKey string) (Item, bool) {
	items, err := l.LoadAll()
	if err != nil {
		return Item{}, false
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].SourceKey == sourceKey {
			return items[i], true
		}
	}
	return Item{}, false
}

// Checksum returns a deterministic sha256 over all items sorted by source_key.
// SCAN order in Redis is implementation-dependent; sorting makes the
// preflight_checksum field stable across runs (doc 15 §2 last sentence).
func (l *Ledger) Checksum() (string, error) {
	items, err := l.LoadAll()
	if err != nil {
		return "", err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].SourceKey < items[j].SourceKey
	})
	h := sha256.New()
	for _, it := range items {
		buf, err := json.Marshal(it)
		if err != nil {
			return "", fmt.Errorf("migration: marshal ledger item: %w", err)
		}
		h.Write(buf)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Reset removes the on-disk file. Tests use this to avoid touching t.TempDir
// (which is removed automatically anyway) and to keep paths deterministic.
func (l *Ledger) Reset() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadAll is a convenience wrapper for tests that want to inspect the file
// after a run completes. It returns the raw bytes.
func (l *Ledger) ReadAll() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return os.ReadFile(l.path)
}

// bufio import retained for future streamed decode helpers.
var _ = bufio.NewReader
