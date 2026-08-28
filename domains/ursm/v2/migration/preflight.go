// Package migration hosts L3 machinery for the URSM delimiter-safe Redis
// key migration described in doc 14 §§2-6, doc 15 §§1-4 and doc 16 slices
// 11-13. This file implements the read-only node/window/index/dedup
// preflight slice only: classification, deterministic inventory and
// ledger; no writes to production Redis metadata, no copy, no coverage
// publication and no cleanup. Owners of subsequent slices must freeze
// the metadata key schema, ACL and CAS conventions before any follow-on
// command ships.
package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
)

// SchemaMode identifies which key grammar the authoritative request path
// recognises. Schema mode is orthogonal to URSM_V2_MODE: the latter still
// defines legacy/URSM v2 routing authority (doc 14 §3, doc 15 §4).
type SchemaMode string

const (
	SchemaModeLegacy    SchemaMode = "legacy"
	SchemaModeDual      SchemaMode = "dual"
	SchemaModeCanonical SchemaMode = "canonical"
)

// Valid returns true iff SchemaMode is one of the frozen values above.
func (s SchemaMode) Valid() bool {
	switch s {
	case SchemaModeLegacy, SchemaModeDual, SchemaModeCanonical:
		return true
	}
	return false
}

// Classification values are defined in ledger.go (the canonical declaration
// lives with the NDJSON ledger; preflight shares them). Preflight-specific
// Status* values are also declared there.

// Checkpoint + CheckpointPreflight are declared in metadata.go alongside
// Mode + Validate.

// NodeTuple / WindowTuple / IndexTuple / keyKind / Ledger / Entry /
// Decision / runner / struct-fieldChecksum are all declared in classify.go
// (the canonical implementation of the Preflight slice). This file only
// contributes the per-entry classification helper ClassifyKeyK2 +
// ClassifiedKey for callers that iterate keys themselves rather than
// driving the full Preflight run, plus a free-function Preflight wrapper
// that the higher-level tests and CLI consume.

// Options is the configuration object for the free-function Preflight
// wrapper. classify.go owns a similar Options shape under the same name;
// the field set is identical so callers can switch freely between the
// two entry points.
type Options struct {
	Redis      *redis.Client
	Prefix     string
	Owner      string
	LedgerID   string
	SchemaMode SchemaMode
	BatchSize  int
	Now        func() time.Time
	ScanLimit  int
}

// ============================================================================
// Per-entry classification helper retained from the feature branch (parallel
// API style: callers iterate the report themselves). Both implementations
// agree on the verdict enum and the (legacy-only) segment-count rules; the
// ledger-driven runner above additionally cross-checks against an existing
// canonical target (doc 14 §4, doc 15 §2).
// ============================================================================

// ============================================================================
// Free-function Preflight wrapper — main's classify.go exposes a struct
// method (*Preflight).Preflight(ctx), but several callers (and tests) use
// the higher-level free function Preflight(ctx, Options{...}) form. The
// wrapper here adapts the struct method by building an in-memory ledger
// from the Redis scan + classify path and folding the result into a
// *Report. Both APIs stay available.
// ============================================================================

// Report is the per-run summary that drives the GO / NO-GO decision. It
// aggregates a Preflight run's counts + decisions + reasons so callers
// don't have to walk the ledger manually.
type Report struct {
	Ledger          PreflightLedger
	Counts          map[Classification]int
	Decisions       []PreflightDecision
	MigrationGoable bool
	Reasons         []string
}

// PreflightLedger is the in-memory ledger carried inside Report. It is
// structurally compatible with the on-disk ledger in ledger.go; the two
// share Classification / Entry / ItemStatus enums.
type PreflightLedger struct {
	Owner             string
	LedgerID          string
	SchemaMode        SchemaMode
	Prefix            string
	StartedAt         time.Time
	UpdatedAt         time.Time
	ScanRunID         string
	Checkpoint        Checkpoint
	Entries           []PreflightEntry
	PreflightChecksum string
}

// PreflightEntry mirrors the classify.go Entry struct shape used by the
// free-function wrapper. Field names are kept identical so callers can
// switch freely between the two implementations.
type PreflightEntry struct {
	SourceKey            string
	KeyKind              string
	KeyType              string
	Schema               store.Schema
	Classification       Classification
	ClassificationReason string
	Status               EntryStatus
	Generation           string
	FieldChecksum        string
	Tuple                *store.ParsedNodeKey
	TargetKey            string
	MemberCount          int64
	MemberChecksum       string
	PTTLMS               int64
	RunID                string
	ReadAt               time.Time
	ScanBatch            int
}

// PreflightDecision is the per-source-key contribution to the verdict.
type PreflightDecision struct {
	SourceKey      string
	Classification Classification
	Reason         string
}

// Preflight runs the read-only classification pass using the free-function
// Options form. It builds an in-memory ledger from the Redis scan +
// classify path, then folds the result into a *Report. The implementation
// defers to the same classify path used by the struct method in classify.go.
func RunPreflight(ctx context.Context, opts Options) (*Report, error) {
	if opts.Redis == nil {
		return nil, fmt.Errorf("migration preflight: Redis client is required")
	}
	if opts.Prefix == "" {
		return nil, fmt.Errorf("migration preflight: prefix is required")
	}
	if opts.Owner == "" {
		return nil, fmt.Errorf("migration preflight: owner is required")
	}
	if opts.LedgerID == "" {
		return nil, fmt.Errorf("migration preflight: ledger_id is required")
	}
	if !isLedgerID(opts.LedgerID) {
		return nil, fmt.Errorf("migration preflight: ledger_id %q is not a one-shot UUIDv4-style identifier", opts.LedgerID)
	}
	if opts.SchemaMode == "" {
		opts.SchemaMode = SchemaModeLegacy
	}
	if !opts.SchemaMode.Valid() {
		return nil, fmt.Errorf("migration preflight: unknown schema mode %q", opts.SchemaMode)
	}
	if opts.ScanLimit < 0 {
		return nil, fmt.Errorf("migration preflight: scan limit %d must be non-negative", opts.ScanLimit)
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	startedAt := nowFn().UTC()
	runID := fmt.Sprintf("preflight-%d", startedAt.UnixNano())

	// SCAN all source patterns that doc 15 §2 mandates. We deliberately use
	// a minimal here so the wrapper does not duplicate the classification
	// logic in classify.go; the full struct-method path remains the
	// canonical one. This wrapper keeps the free-function call sites alive.
	patterns := []string{
		opts.Prefix + "node:*",
		opts.Prefix + "win:*",
		opts.Prefix + "idx:model:*",
		opts.Prefix + "binding:*",
		opts.Prefix + "credential:*",
		opts.Prefix + "provider:*",
		opts.Prefix + "*request_dedup*",
	}
	type sk struct {
		SourceKey     string
		KeyType       string
		PTTLMs        int64
		Gen           string
		FieldChecksum string
	}
	var scanned []sk
	for _, p := range patterns {
		var cursor uint64
		for {
			batch, next, err := opts.Redis.Scan(ctx, cursor, p, 200).Result()
			if err != nil {
				return nil, fmt.Errorf("migration preflight: scan %s: %w", p, err)
			}
			for _, k := range batch {
				kt, terr := opts.Redis.Type(ctx, k).Result()
				if terr != nil {
					continue
				}
				pttl, perr := opts.Redis.PTTL(ctx, k).Result()
				if perr != nil {
					continue
				}
				var gen, checksum string
				if kt == "hash" {
					f, ferr := redissafe.SafeHGetAll(ctx, opts.Redis, k)
					if ferr != nil {
						if errors.Is(ferr, redissafe.ErrKeyNotFound) {
							continue // the key expired during this scan window
						}
						return nil, fmt.Errorf("migration preflight: read hash %s: %w", k, ferr)
					}
					gen = f["generation"]
					checksum = fieldChecksum(f)
				}
				scanned = append(scanned, sk{SourceKey: k, KeyType: kt, PTTLMs: pttlMillis(pttl), Gen: gen, FieldChecksum: checksum})
			}
			if next == 0 {
				break
			}
			cursor = next
		}
	}
	sort.Slice(scanned, func(i, j int) bool { return scanned[i].SourceKey < scanned[j].SourceKey })

	ledger := PreflightLedger{
		Owner:      opts.Owner,
		LedgerID:   opts.LedgerID,
		SchemaMode: opts.SchemaMode,
		Prefix:     opts.Prefix,
		StartedAt:  startedAt,
		UpdatedAt:  nowFn().UTC(),
		ScanRunID:  runID,
		Checkpoint: CheckpointPreflight,
	}
	counts := map[Classification]int{}
	decisions := make([]PreflightDecision, 0, len(scanned))
	reasons := []string{}
	for _, s := range scanned {
		c := ClassifyKeyK2(opts.Prefix, s.SourceKey)
		var schema store.Schema
		switch c.Schema {
		case store.KeySchemaK2:
			schema = store.SchemaK2
		case store.KeySchemaLegacy:
			if c.Class != ClassificationAmbiguous && c.Class != ClassificationConflict {
				schema = store.SchemaLegacy
			}
		}
		class := c.Class
		status := StatusClassified
		reason := c.Reason
		target := s.SourceKey
		// For a migratable legacy tuple, the canonical target is the k2
		// node key derived from the recovered tuple. Without this the
		// ledger cannot drive the copy phase (doc 14 §4, doc 15 §2).
		if class == ClassificationMigratable && c.Tuple != nil {
			if k2, err := store.K2NodeKeyForTenant(opts.Prefix, c.Tuple.TenantID, c.Tuple.CredentialID, c.Tuple.RawModel); err == nil {
				target = k2
			}
		}
		// Schema mode gate: in canonical mode every non-k2 source is excluded
		// (legacy keys are out of scope, doc 14 §3).
		if opts.SchemaMode == SchemaModeCanonical && schema != store.SchemaK2 {
			class = ClassificationExcludedNonAuthoritative
			status = StatusExcluded
			reason = "schema mode canonical; source not in k2 namespace"
		}
		// Cross-check legacy vs canonical target (doc 14 §4 / doc 15 §2):
		// if the canonical k2 hash already exists with a different
		// generation, the legacy source is in conflict.
		if class == ClassificationMigratable && target != s.SourceKey {
			if exists, _ := opts.Redis.Exists(ctx, target).Result(); exists == 1 {
				canonicalFields, ferr := redissafe.SafeHGetAll(ctx, opts.Redis, target)
				if ferr != nil {
					if !errors.Is(ferr, redissafe.ErrKeyNotFound) {
						return nil, fmt.Errorf("migration preflight: read canonical target %s: %w", target, ferr)
					}
				} else {
					srcGen := s.Gen
					canonGen := canonicalFields["generation"]
					if srcGen != "" && canonGen != "" && srcGen != canonGen {
						class = ClassificationConflict
						status = StatusConflict
						reason = "canonical target exists with diverging generation"
					}
				}

			}
		}
		entry := PreflightEntry{
			SourceKey:            s.SourceKey,
			KeyKind:              detectKeyKindForRun(opts.Prefix, s.SourceKey),
			KeyType:              s.KeyType,
			Schema:               schema,
			Classification:       class,
			ClassificationReason: reason,
			Status:               status,
			Generation:           s.Gen,
			FieldChecksum:        s.FieldChecksum,
			Tuple:                c.Tuple,
			TargetKey:            target,
			PTTLMS:               s.PTTLMs,
			RunID:                runID,
		}
		if c.Class == ClassificationExcludedNonAuthoritative {
			entry.Status = StatusExcluded
		}
		if c.Class == ClassificationAmbiguous || c.Class == ClassificationConflict {
			entry.Status = StatusAmbiguous
		}
		ledger.Entries = append(ledger.Entries, entry)
		counts[class]++
		decisions = append(decisions, PreflightDecision{SourceKey: s.SourceKey, Classification: class, Reason: reason})
		if c.Class == ClassificationAmbiguous || c.Class == ClassificationConflict {
			reasons = append(reasons, fmt.Sprintf("%s: %s (%s)", s.SourceKey, c.Class, c.Reason))
		}
	}
	ledger.PreflightChecksum = checksumEntriesReport(ledger.Entries)
	return &Report{
		Ledger:          ledger,
		Counts:          counts,
		Decisions:       decisions,
		MigrationGoable: counts[ClassificationAmbiguous] == 0 && counts[ClassificationConflict] == 0,
		Reasons:         reasons,
	}, nil
}

// checksumEntriesReport builds the deterministic sha256 over the in-memory
// ledger entries. Same shape as ledger.go:Checksum but operates on the
// PreflightEntry struct so it does not depend on Item ordering.
func checksumEntriesReport(entries []PreflightEntry) string {
	type entrySnapshot struct {
		SourceKey, Reason, Type, Schema, Gen, Target, FC string
		Class, Status, RunID                             string
		PTTL                                             int64
	}
	snaps := make([]entrySnapshot, len(entries))
	for i, e := range entries {
		snaps[i] = entrySnapshot{
			SourceKey: e.SourceKey, Reason: e.ClassificationReason,
			Type: e.KeyType, Schema: string(e.Schema), Gen: e.Generation,
			Target: e.TargetKey, FC: e.FieldChecksum,
			Class: string(e.Classification), Status: string(e.Status),
			RunID: e.RunID, PTTL: e.PTTLMS,
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].SourceKey < snaps[j].SourceKey })
	h := sha256.New()
	for _, s := range snaps {
		h.Write([]byte(s.SourceKey))
		h.Write([]byte{0})
		h.Write([]byte(s.Class))
		h.Write([]byte{0})
		h.Write([]byte(s.Status))
		h.Write([]byte{0})
		h.Write([]byte(s.Reason))
		h.Write([]byte{0})
		h.Write([]byte(s.Schema))
		h.Write([]byte{0})
		h.Write([]byte(s.Type))
		h.Write([]byte{0})
		fmt.Fprintf(h, "%d", s.PTTL)
		h.Write([]byte{0})
		h.Write([]byte(s.Gen))
		h.Write([]byte{0})
		h.Write([]byte(s.Target))
		h.Write([]byte{0})
		h.Write([]byte(s.RunID))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ClassifiedKey is the pure classification verdict for one Redis key,
// returned by ClassifyKeyK2.
type ClassifiedKey struct {
	SourceKey string
	Class     Classification
	Reason    string
	Schema    store.KeySchema
	// Tuple is set only for migratable and canonical_present keys —
	// ambiguous keys never carry a guessed tuple.
	Tuple *store.ParsedNodeKey
}

// ClassifyKeyK2 applies the doc 14 §4 / doc 15 §2 rules to one key:
//   - derived dedup markers and non-target namespaces are excluded;
//   - canonical keys are canonical_present;
//   - a legacy node/window key is migratable only when the segment count
//     exactly matches one known grammar form (so no variable segment can
//     contain ':'), the credential is a positive decimal, the tenant is
//     non-empty (canonical has no empty-tenant representation) and
//     re-encoding the recovered tuple with the legacy constructor
//     reproduces the exact source bytes;
//   - everything else is ambiguous — the fail-closed default.
func ClassifyKeyK2(prefix, key string) ClassifiedKey {
	if strings.Contains(key, ":request_dedup:") {
		return excludedK2(key, "derived request dedup marker")
	}
	for _, ns := range []string{"binding:", "credential:", "provider:", "meta:"} {
		if strings.HasPrefix(key, prefix+ns) {
			return excludedK2(key, "non-target namespace "+ns)
		}
	}
	if strings.HasPrefix(key, prefix+"node:") {
		return classifyNodeKeyK2(prefix, key)
	}
	if strings.HasPrefix(key, prefix+"win:") {
		return classifyWindowKeyK2(prefix, key)
	}
	if strings.HasPrefix(key, prefix+"idx:model:") {
		return excludedK2(key, "candidate index is rebuilt, not copied")
	}
	return excludedK2(key, "non-target key")
}

func classifyNodeKeyK2(prefix, key string) ClassifiedKey {
	if strings.HasPrefix(key, prefix+"node:k2:") {
		if p, ok := store.ParseNodeKeyAny(prefix, key); ok && p.Schema == store.KeySchemaK2 {
			t := p.ParsedNodeKey
			return ClassifiedKey{SourceKey: key, Class: ClassificationCanonicalPresent, Reason: "canonical node key", Schema: store.KeySchemaK2, Tuple: &t}
		}
		// Malformed reserved k2: ambiguous verdict with no schema — the
		// reserved namespace must fail closed (doc 14 §2).
		return ClassifiedKey{SourceKey: key, Class: ClassificationAmbiguous, Reason: "malformed canonical node key"}
	}
	parts := strings.Split(strings.TrimPrefix(key, prefix+"node:"), ":")
	var tenant, cidStr, raw string
	switch {
	case len(parts) == 4 && parts[0] == "t" && isAllDigitsK2(parts[1]) && parts[1] != "":
		tenant, cidStr, raw = parts[1], parts[2], parts[3]
	case len(parts) == 3 && parts[0] != "" && parts[0] != "t" && !isAllDigitsK2(parts[0]):
		tenant, cidStr, raw = parts[0], parts[1], parts[2]
	case len(parts) == 2:
		tenant, cidStr, raw = "", parts[0], parts[1]
	default:
		return ambiguousK2(key, "segment count matches no legacy grammar form exactly")
	}
	if cid := parsePositiveDecimalK2(cidStr); cid <= 0 {
		return ambiguousK2(key, "credential segment is not a positive decimal")
	}
	if tenant == "" {
		return ambiguousK2(key, "empty tenant has no canonical representation (operator mapping required)")
	}
	cid, _ := strconv.Atoi(cidStr)
	if store.NodeKeyForTenant(prefix, tenant, cid, raw) != key {
		return ambiguousK2(key, "tuple does not round-trip to the exact source bytes")
	}
	return ClassifiedKey{
		SourceKey: key,
		Class:     ClassificationMigratable,
		Reason:    "unique tuple, strict segments, exact round-trip",
		Schema:    store.KeySchemaLegacy,
		Tuple:     &store.ParsedNodeKey{TenantID: tenant, CredentialID: cid, RawModel: raw},
	}
}

func classifyWindowKeyK2(prefix, key string) ClassifiedKey {
	if strings.HasPrefix(key, prefix+"win:k2:") {
		return ClassifiedKey{SourceKey: key, Class: ClassificationCanonicalPresent, Reason: "canonical window key", Schema: store.KeySchemaK2}
	}
	rest := strings.TrimPrefix(key, prefix+"win:")
	bucket := rest
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		bucket = rest[:i]
	} else {
		return ambiguousK2(key, "window key has no bucket segment")
	}
	switch bucket {
	case "1m", "5m", "30m":
	default:
		return ambiguousK2(key, "window bucket is not one of 1m/5m/30m")
	}
	parts := strings.Split(strings.TrimPrefix(rest, bucket+":"), ":")
	var tenant, cidStr, raw string
	switch {
	case len(parts) == 4 && parts[0] == "t" && isAllDigitsK2(parts[1]) && parts[1] != "":
		tenant, cidStr, raw = parts[1], parts[2], parts[3]
	case len(parts) == 3 && parts[0] != "" && parts[0] != "t" && !isAllDigitsK2(parts[0]):
		tenant, cidStr, raw = parts[0], parts[1], parts[2]
	default:
		return ambiguousK2(key, "window segment count matches no legacy grammar form exactly")
	}
	if parsePositiveDecimalK2(cidStr) <= 0 {
		return ambiguousK2(key, "window credential segment is not a positive decimal")
	}
	if tenant == "" {
		return ambiguousK2(key, "window key has empty tenant (operator mapping required)")
	}
	cid, _ := strconv.Atoi(cidStr)
	if store.WindowKeyForTenant(prefix, tenant, cid, raw, bucket) != key {
		return ambiguousK2(key, "window tuple does not round-trip to the exact source bytes")
	}
	return ClassifiedKey{
		SourceKey: key,
		Class:     ClassificationMigratable,
		Reason:    "unique window tuple, strict segments, exact round-trip",
		Schema:    store.KeySchemaLegacy,
		Tuple:     &store.ParsedNodeKey{TenantID: tenant, CredentialID: cid, RawModel: raw},
	}
}

func excludedK2(key, reason string) ClassifiedKey {
	return ClassifiedKey{SourceKey: key, Class: ClassificationExcludedNonAuthoritative, Reason: reason}
}

func ambiguousK2(key, reason string) ClassifiedKey {
	return ClassifiedKey{SourceKey: key, Class: ClassificationAmbiguous, Reason: reason}
}

func isAllDigitsK2(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parsePositiveDecimalK2(s string) int {
	if !isAllDigitsK2(s) || (len(s) > 1 && s[0] == '0') {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

// detectKeyKindForRun returns the same labels as classify.go's
// detectKeyKind, but using the exported Kind* constants so the free-function
// RunPreflight can populate PreflightEntry.KeyKind without depending on
// the unexported keyKind type.
func detectKeyKindForRun(prefix, key string) string {
	switch {
	case strings.HasPrefix(key, prefix+"node:"):
		return KindNode
	case strings.HasPrefix(key, prefix+"win:"):
		return KindWindow
	case strings.HasPrefix(key, prefix+"idx:model:"):
		return KindIndex
	case strings.HasPrefix(key, prefix+"binding:"):
		return KindBinding
	case strings.HasPrefix(key, prefix+"credential:"):
		return KindCredential
	case strings.HasPrefix(key, prefix+"provider:"):
		return KindProvider
	case strings.Contains(key, "request_dedup"):
		return KindDedup
	}
	return KindUnrecognised
}
