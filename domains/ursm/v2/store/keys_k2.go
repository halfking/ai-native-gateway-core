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

// ParsedNodeKeyWithSchema is a node key tuple plus the grammar that
// produced it. persist, recovery and coverage code must consume the
// schema origin explicitly instead of treating any successful parse as
// authoritative (doc 14 §2).
type ParsedNodeKeyWithSchema struct {
	Schema KeySchema
	ParsedNodeKey
}

// k2DecodeSegment strictly decodes one raw unpadded base64url segment.
// Decoding alone can accept some non-canonical encodings, so the decoded
// bytes must re-encode to the exact input segment.
func k2DecodeSegment(seg string) (string, bool) {
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
