package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	"github.com/redis/go-redis/v9"
)

// ScannedKey is the lightweight result of one SCAN page entry that the
// classifier needs. We accept this shape (rather than *redis.Client) so
// the same logic can run against a fake or a miniredis-backed pipeline.
type ScannedKey struct {
	SourceKey  string
	KeyType    string
	PTTLMs     int64
	Fields     map[string]string
	Generation int64
}

// Scanner abstracts SCAN so tests can inject a deterministic cursor order
// (15 §2 "deterministic checksum" depends on a sorted ledger, not on the
// SCAN cursor sequence).
type Scanner interface {
	Scan(ctx context.Context, pattern string) ([]ScannedKey, error)
}

// RedisScanner implements Scanner over *redis.Client. The pattern argument
// is the exact SCAN pattern; the implementation paginates internally with a
// small COUNT (matches recovery/manager.go:270-287) and returns the union.
type RedisScanner struct {
	RDB     *redis.Client
	Count   int
}

func (s *RedisScanner) Scan(ctx context.Context, pattern string) ([]ScannedKey, error) {
	if s.RDB == nil {
		return nil, fmt.Errorf("migration: redis scanner: nil client")
	}
	count := s.Count
	if count <= 0 {
		count = 200
	}
	var (
		cursor    uint64
		out       []ScannedKey
	)
	for {
		keys, next, err := s.RDB.Scan(ctx, cursor, pattern, int64(count)).Result()
		if err != nil {
			return nil, fmt.Errorf("migration: scan %s: %w", pattern, err)
		}
		for _, k := range keys {
			t, terr := s.RDB.Type(ctx, k).Result()
			if terr != nil {
				return nil, fmt.Errorf("migration: type %s: %w", k, terr)
			}
			pttl, perr := s.RDB.PTTL(ctx, k).Result()
			if perr != nil {
				return nil, fmt.Errorf("migration: pttl %s: %w", k, perr)
			}
			var fields map[string]string
			var gen int64
				if t == "hash" {
					// audit-24h-20260828-r4 P2: Use SafeHGetAll to prevent
					// WRONGTYPE. The preflight scanned kind=="hash" so a
					// TypedError here is a TOCTOU race. ErrKeyNotFound
					// collapses to empty fields.
					hf, ferr := redissafe.SafeHGetAll(ctx, s.RDB, k)
					if ferr != nil {
						if errors.Is(ferr, redissafe.ErrKeyNotFound) {
							hf = map[string]string{}
						} else {
							return nil, fmt.Errorf("migration: hgetall %s: %w", k, ferr)
						}
					}
					fields = hf
					if g, ok := hf["generation"]; ok {
						gen = parseInt64OrZero(g)
					}
				}
			out = append(out, ScannedKey{
				SourceKey:  k,
				KeyType:    t,
				PTTLMs:     pttl.Milliseconds(),
				Fields:     fields,
				Generation: gen,
			})
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

func parseInt64OrZero(s string) int64 {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

// Preflight runs the read-only classification pass (doc 14 §4 / 15 §2).
// It scans the three URSM key namespaces, classifies each key into one of
// the four categories, computes a per-item field checksum, and writes the
// outcome to the ledger. The returned summary includes the deterministic
// preflight_checksum that becomes Metadata.PreflightChecksum.
type PreflightRunner struct {
	Prefix  string
	Ledger  *Ledger
	Scanner Scanner
	RunID   string
	Now     func() time.Time
}

// (Preflight type alias removed; callers now use PreflightRunner directly.)

// PreflightSummary is the aggregate result of a preflight pass.
type PreflightSummary struct {
	TotalKeys       int
	Migratable      int
	CanonicalPresent int
	Ambiguous       int
	Excluded        int
	Conflicts       int
	Checksum        string
}

// ClassifyResult mirrors the per-item ledger row plus a few helpers used by
// copy/cleanup. Keeping it separate from Item avoids accidental schema
// drift between the on-disk format and the in-memory working set.
type ClassifyResult struct {
	Item     Item
	Copyable bool
}

func (p *PreflightRunner) scannerOrDefault() Scanner { return p.Scanner }

// Preflight is the public entry point. It scans node/win/idx namespaces and
// writes classified items to the ledger. The first error short-circuits but
// already-appended items stay on disk (so resume works).
func (p *PreflightRunner) Preflight(ctx context.Context) (PreflightSummary, error) {
	if p.Prefix == "" {
		return PreflightSummary{}, fmt.Errorf("migration: preflight: prefix is required")
	}
	if p.Ledger == nil {
		return PreflightSummary{}, fmt.Errorf("migration: preflight: ledger is required")
	}
	sc := p.scannerOrDefault()
	if sc == nil {
		return PreflightSummary{}, fmt.Errorf("migration: preflight: scanner is required")
	}
	nowFn := p.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	runID := p.RunID
	if runID == "" {
		runID = fmt.Sprintf("preflight-%d", nowFn().UnixNano())
	}
	patterns := []string{
		p.Prefix + "node:*",
		p.Prefix + "win:*",
		p.Prefix + "idx:model:*",
	}
	// Determinism: dedupe + sort SCAN output before classification. Redis
	// SCAN is unordered; the ledger checksum must be reproducible across
	// cursor reseeds (slice 11 red-team property test).
	all := make(map[string]ScannedKey)
	for _, pat := range patterns {
		page, err := sc.Scan(ctx, pat)
		if err != nil {
			return PreflightSummary{}, err
		}
		for _, k := range page {
			all[k.SourceKey] = k
		}
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	summary := PreflightSummary{TotalKeys: len(keys)}
	for _, k := range keys {
		scanned := all[k]
		item := p.classifyOne(ctx, scanned, runID)
		summary.bump(item.Classification, item.Status)
		if err := p.Ledger.Append(item); err != nil {
			return summary, err
		}
	}
	sum, err := p.Ledger.Checksum()
	if err != nil {
		return summary, err
	}
	summary.Checksum = sum
	return summary, nil
}

func (s *PreflightSummary) bump(c Classification, status ItemStatus) {
	switch c {
	case ClassificationMigratable:
		s.Migratable++
	case ClassificationCanonicalPresent:
		s.CanonicalPresent++
	case ClassificationAmbiguous:
		s.Ambiguous++
	case ClassificationExcludedNonAuthoritative:
		s.Excluded++
	}
	if status == StatusConflict {
		s.Conflicts++
	}
}

func (p *PreflightRunner) classifyOne(ctx context.Context, sk ScannedKey, runID string) Item {
	it := Item{
		SourceKey: sk.SourceKey,
		KeyType:   sk.KeyType,
		PTTLMs:    sk.PTTLMs,
		Status:    StatusDiscovered,
		ScanRunID: runID,
		FieldChecksum: fieldChecksum(sk.Fields),
		Generation:    sk.Generation,
	}
	// Non-authoritative namespaces (doc 14 §4 + 15 §2): binding, credential,
	// provider, and the request_dedup: suffix are never migration targets.
	if isExcludedNamespace(p.Prefix, sk.SourceKey) {
		it.Classification = ClassificationExcludedNonAuthoritative
		it.Reason = "non-authoritative namespace"
		it.Status = StatusExcluded
		return it
	}
	// Empty source: cannot classify, mark ambiguous so the run stays NO-GO.
	if sk.KeyType == "" {
		it.Classification = ClassificationAmbiguous
		it.Reason = "empty key type"
		it.Status = StatusAmbiguous
		return it
	}
	// k2 keys that already exist in the new grammar: detect and reclassify.
	// The L1 grammar is the only authoritative way to recognize them.
	if parsed, schema, ok := parseAnyNodeKey(p.Prefix, sk.SourceKey); ok {
		it.SchemaSource = string(schema)
		if schema == store.SchemaK2 {
			it.Classification = ClassificationCanonicalPresent
			it.CanonicalKey = sk.SourceKey
			it.Status = StatusClassified
			return it
		}
		// legacy node: round-trip re-encoding must reproduce the exact bytes.
		// The legacy builder is the only allowed inverse; the L1 keys.go
		// exposes NodeKeyForTenant. We import the package but call the
		// legacy re-encoding path inline (avoid pulling the untransferred
		// file at test time). The exact bytes below match keys.go:15-22.
		// Reject cid<=0 before re-encoding so a legacy decoder that
		// happens to accept cid=0 cannot be smuggled into migratable.
		if parsed.CredentialID <= 0 || parsed.RawModel == "" {
			it.Classification = ClassificationAmbiguous
			it.Reason = "round-trip mismatch"
			it.Status = StatusAmbiguous
			return it
		}
		encoded := legacyNodeKeyForTenant(p.Prefix, parsed.TenantID, parsed.CredentialID, parsed.RawModel)
		if encoded != sk.SourceKey {
			it.Classification = ClassificationAmbiguous
			it.Reason = "round-trip mismatch"
			it.Status = StatusAmbiguous
			return it
		}
		canonical := store.NodeKeyCanonical(p.Prefix, parsed.TenantID, parsed.CredentialID, parsed.RawModel)
		// If the canonical key already exists with a different generation
		// or field checksum, the run is in conflict (doc 14 §4).
		// (Implementation reuses the scanned fields if the source IS the
		// canonical key — otherwise copy/cleanup will re-check.)
		it.CanonicalKey = canonical
		it.Classification = ClassificationMigratable
		it.Status = StatusClassified
		return it
	}
	// Fallback: unparseable key (e.g., legacy window, idx, dedup) — still
	// require further slice-9 work to map. For L3 slice 11 we keep them in
	// the ledger as "excluded" with a clear reason, since the authoritative
	// window/index migration is the URSM owner's slice 9.
	if strings.HasPrefix(sk.SourceKey, p.Prefix+"win:") ||
		strings.HasPrefix(sk.SourceKey, p.Prefix+"idx:model:") {
		it.Classification = ClassificationExcludedNonAuthoritative
		it.Reason = "schema-aware classification pending URSM owner slice 9"
		it.Status = StatusExcluded
		return it
	}
	it.Classification = ClassificationAmbiguous
	it.Reason = "unparseable source key"
	it.Status = StatusAmbiguous
	return it
}

// parseAnyNodeKey prefers the L1 canonical parser (k2 first) and falls back
// to the L1 legacy parser exported from keys_k2.go.
func parseAnyNodeKey(prefix, key string) (store.ParsedNodeKey, store.Schema, bool) {
	if parsed, schema, ok := store.ParseNodeKeyCanonical(prefix, key); ok {
		return parsed, schema, true
	}
	return store.ParsedNodeKey{}, "", false
}

// legacyNodeKeyForTenant mirrors the byte-exact output of store.NodeKeyForTenant
// (keys.go:15-23). We re-implement it here so the migration package does not
// import any URSM file outside the 14 §0 handover list. Behavior:
//   - empty tenant -> NodeKey (no tenant segment)
//   - non-numeric tenant -> <prefix>node:<tenant>:<cid>:<raw>
//   - numeric tenant -> <prefix>node:t:<tenant>:<cid>:<raw>
func legacyNodeKeyForTenant(prefix, tenant string, cid int, raw string) string {
	if tenant == "" {
		return fmt.Sprintf("%snode:%d:%s", prefix, cid, raw)
	}
	numeric := true
	if tenant == "" {
		numeric = false
	} else {
		for _, r := range tenant {
			if r < '0' || r > '9' {
				numeric = false
				break
			}
		}
	}
	if !numeric {
		return fmt.Sprintf("%snode:%s:%d:%s", prefix, tenant, cid, raw)
	}
	return fmt.Sprintf("%snode:t:%s:%d:%s", prefix, tenant, cid, raw)
}

// isExcludedNamespace returns true for keys that are not authoritative URSM
// routing state (doc 15 §2 "excluded_non_authoritative").
func isExcludedNamespace(prefix, key string) bool {
	suffixes := []string{
		prefix + "binding:",
		prefix + "credential:",
		prefix + "provider:",
		prefix + "meta:",
	}
	for _, s := range suffixes {
		if strings.HasPrefix(key, s) {
			return true
		}
	}
	// request_dedup keys are derived from node key (record_request.go:102-105).
	if strings.Contains(key, ":request_dedup:") {
		return true
	}
	return false
}

// fieldChecksum returns a deterministic sha256 over the field map sorted by
// key. doc 15 §2 says "name\x00value" is the canonical serialization; we
// use that exact byte sequence.
func fieldChecksum(fields map[string]string) string {
	if len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(fields[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}
