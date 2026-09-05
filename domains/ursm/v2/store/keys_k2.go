package store

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// KeySchema identifies which grammar a Redis node key belongs to. The two
// grammars coexist during the k2 migration (doc 14 §2); every parser that
// feeds authoritative state must report which one produced a tuple instead
// of treating any successful parse as equivalent.
type KeySchema int

const (
	// KeySchemaLegacy is the frozen legacy `node:` grammar. Its exact
	// output bytes are a compatibility contract and never change.
	KeySchemaLegacy KeySchema = iota
	// KeySchemaK2 is the versioned canonical grammar
	// `<prefix>node:k2:<b64url-tenant>:<credential-id>:<b64url-raw-model>`
	// with no hash-tag (standalone-only topology, doc 14 §10).
	KeySchemaK2
)

// String returns the wire string for a KeySchema. Unknown values default
// to legacy so a stale database row never gets misreported as canonical.
func (s KeySchema) String() string {
	switch s {
	case KeySchemaK2:
		return "k2"
	default:
		return "legacy"
	}
}

// ParseKeySchema returns the KeySchema for the wire form; unknown values
// default to legacy so a stale row is never misreported as canonical.
func ParseKeySchema(s string) KeySchema {
	switch s {
	case "k2":
		return KeySchemaK2
	default:
		return KeySchemaLegacy
	}
}

// k2EncodeSegment encodes one variable key component with raw unpadded
// base64url so a component can never contain the `:` delimiter and every
// delimiter-bearing value round-trips losslessly.
func k2EncodeSegment(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// K2NodeKeyForTenant builds the canonical node hash key for one
// (tenant, credential, raw model) tuple. The canonical grammar never
// represents an empty tenant (doc 14 §2); the legacy compatibility
// constructors keep that case.
func K2NodeKeyForTenant(prefix, tenant string, cid int, raw string) (string, error) {
	if err := k2ValidateNodeTuple(tenant, cid, raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("%snode:k2:%s:%d:%s", prefix, k2EncodeSegment(tenant), cid, k2EncodeSegment(raw)), nil
}

// K2WindowKeyForTenant builds the canonical window ZSET key for one
// (tenant, credential, raw model, bucket) tuple. Buckets are frozen to
// 1m, 5m and 30m (doc 14 §2).
func K2WindowKeyForTenant(prefix, tenant string, cid int, raw, bucket string) (string, error) {
	if err := k2ValidateBucket(bucket); err != nil {
		return "", err
	}
	if err := k2ValidateNodeTuple(tenant, cid, raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("%swin:k2:%s:%s:%d:%s", prefix, bucket, k2EncodeSegment(tenant), cid, k2EncodeSegment(raw)), nil
}

// K2CandidateIndexKey builds the canonical candidate index ZSET key. The
// candidate index has no production routing caller; its grammar is still a
// public compatibility contract (doc 14 §2).
func K2CandidateIndexKey(prefix, tenant, canonical, profile, modality string) (string, error) {
	// Ordered checks keep the error message deterministic when several
	// components are empty at once.
	for _, c := range []struct{ name, part string }{
		{"tenant", tenant},
		{"canonical model", canonical},
		{"profile", profile},
		{"modality", modality},
	} {
		if c.part == "" {
			return "", fmt.Errorf("ursm.v2: k2 candidate index key requires non-empty %s", c.name)
		}
	}
	return fmt.Sprintf("%sidx:model:k2:%s:%s:%s:%s",
		prefix, k2EncodeSegment(tenant), k2EncodeSegment(canonical), k2EncodeSegment(profile), k2EncodeSegment(modality)), nil
}

// k2ValidateNodeTuple enforces the grammar constraints shared by the node
// and window constructors: non-empty tenant (canonical never represents
// the empty tenant), positive decimal credential ID, non-empty raw model.
func k2ValidateNodeTuple(tenant string, cid int, raw string) error {
	if tenant == "" {
		return fmt.Errorf("ursm.v2: k2 key requires non-empty tenant (empty tenant is legacy-compatibility only, doc 14 §2)")
	}
	if cid <= 0 {
		return fmt.Errorf("ursm.v2: k2 key requires positive credential id, got %d", cid)
	}
	if raw == "" {
		return fmt.Errorf("ursm.v2: k2 key requires non-empty raw model")
	}
	return nil
}

// k2ValidateBucket enforces the frozen window bucket set shared by the k2
// constructor and any future k2 window parser.
func k2ValidateBucket(bucket string) error {
	switch bucket {
	case "1m", "5m", "30m":
		return nil
	}
	return fmt.Errorf("ursm.v2: k2 window bucket %q is not one of the frozen 1m/5m/30m", bucket)
}

// K2KeySet mirrors NodeKeySet for the canonical grammar: the k2 node hash
// and the three k2 window ZSET keys for one logical tuple.
type K2KeySet struct {
	Node   string
	Win1m  string
	Win5m  string
	Win30m string
}

// K2KeySetForTenant derives the canonical key set for a tuple already
// expressed as a legacy NodeKeySet. Tuples the canonical grammar cannot
// represent (empty tenant) return an error; callers fall back to the
// legacy set for those (doc 14 §2).
func K2KeySetForTenant(prefix, tenant string, cid int, raw string) (K2KeySet, error) {
	if err := k2ValidateNodeTuple(tenant, cid, raw); err != nil {
		return K2KeySet{}, err
	}
	node, err := K2NodeKeyForTenant(prefix, tenant, cid, raw)
	if err != nil {
		return K2KeySet{}, err
	}
	w1, err := K2WindowKeyForTenant(prefix, tenant, cid, raw, "1m")
	if err != nil {
		return K2KeySet{}, err
	}
	w5, err := K2WindowKeyForTenant(prefix, tenant, cid, raw, "5m")
	if err != nil {
		return K2KeySet{}, err
	}
	w30, err := K2WindowKeyForTenant(prefix, tenant, cid, raw, "30m")
	if err != nil {
		return K2KeySet{}, err
	}
	return K2KeySet{Node: node, Win1m: w1, Win5m: w5, Win30m: w30}, nil
}

// ParsedNodeKeyWithSchema is a node key tuple plus the grammar that
// produced it. persist, recovery and coverage code must consume the
// schema origin explicitly instead of treating any successful parse as
// authoritative (doc 14 §2).
type ParsedNodeKeyWithSchema struct {
	Schema KeySchema
	ParsedNodeKey
}

// k2DecodeSegment strictly decodes one raw unpadded base64url segment.
// Empty segments, invalid alphabet and non-canonical trailing bits all
// fail: decoding alone can accept some non-canonical encodings, so the
// decoded bytes must re-encode to the exact input segment.
func k2DecodeSegment(seg string) (string, bool) {
	if seg == "" {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return "", false
	}
	if base64.RawURLEncoding.EncodeToString(b) != seg {
		return "", false
	}
	return string(b), true
}

// isCanonicalDecimalCID reports whether s is the exact decimal rendering
// of a positive credential ID (digits only, no leading zero, no sign).
func isCanonicalDecimalCID(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParseNodeKeyAny parses both the legacy and the k2 node grammar and
// reports which schema produced the tuple. The k2 branch is strict —
// exact segment count, canonical base64url segments, canonical decimal
// credential ID — with no rejoin and no fallback between grammars.
func ParseNodeKeyAny(prefix, key string) (ParsedNodeKeyWithSchema, bool) {
	if rest, ok := strings.CutPrefix(key, prefix+"node:k2:"); ok {
		parts := strings.Split(rest, ":")
		if len(parts) != 3 {
			return ParsedNodeKeyWithSchema{}, false
		}
		tenant, ok := k2DecodeSegment(parts[0])
		if !ok || tenant == "" {
			return ParsedNodeKeyWithSchema{}, false
		}
		if !isCanonicalDecimalCID(parts[1]) {
			return ParsedNodeKeyWithSchema{}, false
		}
		cid, err := strconv.Atoi(parts[1])
		if err != nil || cid <= 0 {
			return ParsedNodeKeyWithSchema{}, false
		}
		raw, ok := k2DecodeSegment(parts[2])
		if !ok || raw == "" {
			return ParsedNodeKeyWithSchema{}, false
		}
		return ParsedNodeKeyWithSchema{
			Schema:        KeySchemaK2,
			ParsedNodeKey: ParsedNodeKey{TenantID: tenant, CredentialID: cid, RawModel: raw},
		}, true
	}
	parsed, ok := ParseNodeKey(prefix, key)
	if !ok {
		return ParsedNodeKeyWithSchema{}, false
	}
	return ParsedNodeKeyWithSchema{Schema: KeySchemaLegacy, ParsedNodeKey: parsed}, true
}

// k2WindowBuckets is the frozen window bucket vocabulary (doc 14 §2).
var k2WindowBuckets = map[string]bool{"1m": true, "5m": true, "30m": true}

// Schema is the string wire form of the node key grammar. Two grammars
// coexist during the k2 migration (doc 14 §2); the wire form must be
// stable across PG / Redis / log paths.
type Schema string

const (
	SchemaLegacy Schema = "legacy"
	SchemaK2     Schema = "k2"
)

// ParseNodeKeyCanonical decodes node keys of both grammars and reports the
// schema source next to the tuple. k2 is probed first: once the k2 marker
// exists it owns the node:k2: namespace, so a key matching both grammars
// resolves as k2. Decoding is strict — exact segment count, canonical
// base64url segments, positive decimal credential — with no lenient
// fallback and no guessing of ambiguous legacy tuples.
func ParseNodeKeyCanonical(prefix, key string) (ParsedNodeKey, Schema, bool) {
	if strings.HasPrefix(key, prefix+"node:k2:") {
		parsed, ok := parseNodeKeyK2(prefix, key)
		if !ok {
			return ParsedNodeKey{}, "", false
		}
		return parsed, SchemaK2, true
	}
	if parsed, ok := ParseNodeKey(prefix, key); ok {
		return parsed, SchemaLegacy, true
	}
	return ParsedNodeKey{}, "", false
}

// parseNodeKeyK2 strictly decodes the k2 grammar emitted by
// NodeKeyCanonical; legacy node keys are not recognized here. This wraps
// k2DecodeSegment from the canonical primitive set so the two k2 entry
// points stay byte-equivalent on a round-trip (doc 15 §2).
func parseNodeKeyK2(prefix, key string) (ParsedNodeKey, bool) {
	base := prefix + "node:k2:"
	if !strings.HasPrefix(key, base) {
		return ParsedNodeKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(key, base), ":")
	if len(parts) != 3 {
		return ParsedNodeKey{}, false
	}
	tenant, ok := k2DecodeSegment(parts[0])
	if !ok || tenant == "" {
		return ParsedNodeKey{}, false
	}
	if !isCanonicalDecimalCID(parts[1]) {
		return ParsedNodeKey{}, false
	}
	credentialID, err := strconv.Atoi(parts[1])
	if err != nil || credentialID <= 0 {
		return ParsedNodeKey{}, false
	}
	raw, ok := k2DecodeSegment(parts[2])
	if !ok || raw == "" {
		return ParsedNodeKey{}, false
	}
	return ParsedNodeKey{TenantID: tenant, CredentialID: credentialID, RawModel: raw}, true
}
// ParsedWindowKey is the tuple recovered from a canonical k2 window key.
type ParsedWindowKey struct {
	Bucket       string
	TenantID     string
	CredentialID int
	RawModel     string
}

// ParseWindowKeyAny strictly decodes the k2 window key. Legacy window
// keys are not recognized here; callers needing both grammars must try
// ParseNodeKeyAny first.
func ParseWindowKeyAny(prefix, key string) (ParsedWindowKey, bool) {
	baseKey := prefix + "win:k2:"
	if !strings.HasPrefix(key, baseKey) {
		return ParsedWindowKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(key, baseKey), ":")
	if len(parts) != 4 {
		return ParsedWindowKey{}, false
	}
	bucket := parts[0]
	if !k2WindowBuckets[bucket] {
		return ParsedWindowKey{}, false
	}
	tenant, ok := k2DecodeSegment(parts[1])
	if !ok {
		return ParsedWindowKey{}, false
	}
	if !isCanonicalDecimalCID(parts[2]) {
		return ParsedWindowKey{}, false
	}
	credentialID, err := strconv.Atoi(parts[2])
	if err != nil || credentialID <= 0 {
		return ParsedWindowKey{}, false
	}
	raw, ok := k2DecodeSegment(parts[3])
	if !ok || raw == "" {
		return ParsedWindowKey{}, false
	}
	return ParsedWindowKey{Bucket: bucket, TenantID: tenant, CredentialID: credentialID, RawModel: raw}, true
}

// ParsedCandidateIndexKey is the tuple recovered from a canonical k2
// candidate index key.
type ParsedCandidateIndexKey struct {
	TenantID       string
	CanonicalModel string
	Profile        string
	Modality       string
}

// ParseCandidateIndexKeyAny strictly decodes the k2 candidate index key.
func ParseCandidateIndexKeyAny(prefix, key string) (ParsedCandidateIndexKey, bool) {
	baseKey := prefix + "idx:model:k2:"
	if !strings.HasPrefix(key, baseKey) {
		return ParsedCandidateIndexKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(key, baseKey), ":")
	if len(parts) != 4 {
		return ParsedCandidateIndexKey{}, false
	}
	tenant, ok := k2DecodeSegment(parts[0])
	if !ok || tenant == "" {
		return ParsedCandidateIndexKey{}, false
	}
	canonical, ok := k2DecodeSegment(parts[1])
	if !ok || canonical == "" {
		return ParsedCandidateIndexKey{}, false
	}
	profile, ok := k2DecodeSegment(parts[2])
	if !ok || profile == "" {
		return ParsedCandidateIndexKey{}, false
	}
	modality, ok := k2DecodeSegment(parts[3])
	if !ok || modality == "" {
		return ParsedCandidateIndexKey{}, false
	}
	return ParsedCandidateIndexKey{TenantID: tenant, CanonicalModel: canonical, Profile: profile, Modality: modality}, true
}

// ============================================================================
// Canonical constructors / parsers used by the migration package (doc 14
// §0 handover list). These mirror K2NodeKeyForTenant / K2WindowKeyForTenant
// / K2CandidateIndexKey but use the simpler name + ParsedWindowKey return
// shape the migration machinery imports.
// ============================================================================

// NodeKeyCanonical builds the canonical k2 node key. Returns "" when the
// tuple is invalid (empty tenant / non-positive cid / empty raw) so the
// migration code can compare without error handling on the hot path.
func NodeKeyCanonical(prefix, tenant string, cid int, raw string) string {
	k, err := K2NodeKeyForTenant(prefix, tenant, cid, raw)
	if err != nil {
		return ""
	}
	return k
}

// WindowKeyCanonical builds the canonical k2 window key. Returns "" when
// any input is invalid (bucket, tenant, cid, raw) so the migration code
// can compare without error handling.
func WindowKeyCanonical(prefix, tenant string, cid int, raw, bucket string) string {
	k, err := K2WindowKeyForTenant(prefix, tenant, cid, raw, bucket)
	if err != nil {
		return ""
	}
	return k
}

// CandidateIndexKeyCanonical builds the canonical k2 candidate index key.
// Returns "" when any component is empty.
func CandidateIndexKeyCanonical(prefix, tenant, canonical, profile, modality string) string {
	k, err := K2CandidateIndexKey(prefix, tenant, canonical, profile, modality)
	if err != nil {
		return ""
	}
	return k
}

// ParseWindowKeyCanonical strictly decodes the k2 window grammar. Returns
// the parsed tuple + ok; callers needing both grammars must try
// ParseNodeKeyCanonical first.
func ParseWindowKeyCanonical(prefix, key string) (ParsedWindowKey, bool) {
	return ParseWindowKeyAny(prefix, key)
}

// ParseCandidateIndexKeyCanonical strictly decodes the k2 candidate index
// grammar.
func ParseCandidateIndexKeyCanonical(prefix, key string) (ParsedCandidateIndexKey, bool) {
	return ParseCandidateIndexKeyAny(prefix, key)
}