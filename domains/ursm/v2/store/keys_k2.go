package store

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Schema identifies which key grammar produced a parsed node key tuple.
// Persist, recovery and coverage code must act on the reported source
// instead of assuming any successful parse is authoritative (doc 14 §2).
type Schema string

const (
	SchemaLegacy Schema = "legacy"
	SchemaK2     Schema = "k2"
)

// NodeKeyCanonical builds the k2 canonical node key frozen in doc 14 §2:
//
//	<prefix>node:k2:<b64url-tenant>:<credential-id>:<b64url-raw-model>
//
// Like the legacy builders it is a pure function. Inputs that are not
// representable in the canonical grammar (empty tenant or raw model,
// non-positive credential) encode to keys ParseNodeKeyCanonical rejects;
// the authoritative read/write path must still reject an empty tenant
// before reaching this function (doc 13 §5.4, doc 14 §2).
func NodeKeyCanonical(prefix, tenant string, cid int, raw string) string {
	return fmt.Sprintf("%snode:k2:%s:%d:%s", prefix, encodeK2Segment(tenant), cid, encodeK2Segment(raw))
}

// WindowKeyCanonical builds the k2 canonical window key frozen in doc 14 §2:
//
//	<prefix>win:k2:<bucket>:<b64url-tenant>:<credential-id>:<b64url-raw-model>
//
// Only the frozen buckets 1m, 5m and 30m survive parsing; any other bucket
// produces a key ParseWindowKeyCanonical rejects.
func WindowKeyCanonical(prefix, tenant string, cid int, raw, bucket string) string {
	return fmt.Sprintf("%swin:k2:%s:%s:%d:%s", prefix, bucket, encodeK2Segment(tenant), cid, encodeK2Segment(raw))
}

// CandidateIndexKeyCanonical builds the k2 canonical candidate index key
// frozen in doc 14 §2:
//
//	<prefix>idx:model:k2:<b64url-tenant>:<b64url-canonical>:<b64url-profile>:<b64url-modality>
//
// Empty components encode to empty segments that parsing rejects.
func CandidateIndexKeyCanonical(prefix, tenant, canonical, profile, modality string) string {
	return fmt.Sprintf("%sidx:model:k2:%s:%s:%s:%s",
		prefix, encodeK2Segment(tenant), encodeK2Segment(canonical), encodeK2Segment(profile), encodeK2Segment(modality))
}

func encodeK2Segment(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// decodeK2Segment inverts encodeK2Segment and rejects anything that is not
// the exact canonical raw unpadded base64url form: empty segments, invalid
// alphabet, padding and non-canonical trailing bits all fail.
func decodeK2Segment(seg string) (string, bool) {
	if seg == "" {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return "", false
	}
	if encodeK2Segment(string(b)) != seg {
		return "", false
	}
	return string(b), true
}

// k2WindowBuckets is the frozen window bucket vocabulary (doc 14 §2).
var k2WindowBuckets = map[string]bool{"1m": true, "5m": true, "30m": true}

// ParseNodeKeyCanonical decodes node keys of both grammars and reports the
// schema source next to the tuple. k2 is probed first: once the k2 marker
// exists it owns the node:k2: namespace, so a key matching both grammars
// resolves as k2. Decoding is strict — exact segment count, canonical
// base64url segments, positive decimal credential — with no lenient
// fallback and no guessing of ambiguous legacy tuples.
func ParseNodeKeyCanonical(prefix, key string) (ParsedNodeKey, Schema, bool) {
	if parsed, ok := parseNodeKeyK2(prefix, key); ok {
		return parsed, SchemaK2, true
	}
	if parsed, ok := ParseNodeKey(prefix, key); ok {
		return parsed, SchemaLegacy, true
	}
	return ParsedNodeKey{}, "", false
}

// parseNodeKeyK2 strictly decodes the k2 grammar emitted by
// NodeKeyCanonical; legacy node keys are not recognized here.
func parseNodeKeyK2(prefix, key string) (ParsedNodeKey, bool) {
	base := prefix + "node:k2:"
	if !strings.HasPrefix(key, base) {
		return ParsedNodeKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(key, base), ":")
	if len(parts) != 3 {
		return ParsedNodeKey{}, false
	}
	tenant, ok := decodeK2Segment(parts[0])
	if !ok {
		return ParsedNodeKey{}, false
	}
	credentialID, err := strconv.Atoi(parts[1])
	if err != nil || credentialID <= 0 || strconv.Itoa(credentialID) != parts[1] {
		return ParsedNodeKey{}, false
	}
	raw, ok := decodeK2Segment(parts[2])
	if !ok {
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

// ParseWindowKeyCanonical strictly decodes the k2 window key emitted by
// WindowKeyCanonical, including the frozen bucket whitelist. Legacy window
// keys are not recognized here.
func ParseWindowKeyCanonical(prefix, key string) (ParsedWindowKey, bool) {
	base := prefix + "win:k2:"
	if !strings.HasPrefix(key, base) {
		return ParsedWindowKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(key, base), ":")
	if len(parts) != 4 {
		return ParsedWindowKey{}, false
	}
	bucket := parts[0]
	if !k2WindowBuckets[bucket] {
		return ParsedWindowKey{}, false
	}
	tenant, ok := decodeK2Segment(parts[1])
	if !ok {
		return ParsedWindowKey{}, false
	}
	credentialID, err := strconv.Atoi(parts[2])
	if err != nil || credentialID <= 0 || strconv.Itoa(credentialID) != parts[2] {
		return ParsedWindowKey{}, false
	}
	raw, ok := decodeK2Segment(parts[3])
	if !ok {
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

// ParseCandidateIndexKeyCanonical strictly decodes the k2 candidate index
// key emitted by CandidateIndexKeyCanonical. Legacy index keys are not
// recognized here.
func ParseCandidateIndexKeyCanonical(prefix, key string) (ParsedCandidateIndexKey, bool) {
	base := prefix + "idx:model:k2:"
	if !strings.HasPrefix(key, base) {
		return ParsedCandidateIndexKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(key, base), ":")
	if len(parts) != 4 {
		return ParsedCandidateIndexKey{}, false
	}
	tenant, ok := decodeK2Segment(parts[0])
	if !ok {
		return ParsedCandidateIndexKey{}, false
	}
	canonical, ok := decodeK2Segment(parts[1])
	if !ok {
		return ParsedCandidateIndexKey{}, false
	}
	profile, ok := decodeK2Segment(parts[2])
	if !ok {
		return ParsedCandidateIndexKey{}, false
	}
	modality, ok := decodeK2Segment(parts[3])
	if !ok {
		return ParsedCandidateIndexKey{}, false
	}
	return ParsedCandidateIndexKey{TenantID: tenant, CanonicalModel: canonical, Profile: profile, Modality: modality}, true
}
