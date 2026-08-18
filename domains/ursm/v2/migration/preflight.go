// Package migration implements the URSM k2 key migration machinery
// (doc 14 §4/§6): read-only preflight classification with a checksummed
// ledger, generation/checksum-fenced copy with PTTL preservation, and
// exact-ledger cleanup. Everything here is Migration-owner territory and
// runs offline from the gateway process (dry-run by default).
package migration

import (
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Classification is the frozen preflight verdict for one source key
// (doc 14 §4). ambiguous or conflict verdicts default the whole migration
// to NO-GO; only an audited operator identity mapping may move a key out
// of ambiguous.
type Classification string

const (
	ClassMigratable               Classification = "migratable"
	ClassCanonicalPresent         Classification = "canonical_present"
	ClassAmbiguous                Classification = "ambiguous"
	ClassExcludedNonAuthoritative Classification = "excluded_non_authoritative"
)

// ClassifiedKey is the pure classification verdict for one Redis key.
type ClassifiedKey struct {
	SourceKey string
	Class     Classification
	Reason    string
	Schema    store.KeySchema
	// Tuple is set only for migratable and canonical_present keys —
	// ambiguous keys never carry a guessed tuple.
	Tuple *store.ParsedNodeKey
}

// ClassifyKey applies the doc 14 §4 / doc 15 §2 rules to one key:
//
//   - derived dedup markers and non-target namespaces are excluded;
//   - canonical keys are canonical_present;
//   - a legacy node/window key is migratable only when the segment count
//     exactly matches one known grammar form (so no variable segment can
//     contain ':'), the credential is a positive decimal, the tenant is
//     non-empty (canonical has no empty-tenant representation) and
//     re-encoding the recovered tuple with the legacy constructor
//     reproduces the exact source bytes;
//   - everything else is ambiguous — the fail-closed default.
func ClassifyKey(prefix, key string) ClassifiedKey {
	if strings.Contains(key, ":request_dedup:") {
		return excluded(key, "derived request dedup marker")
	}
	for _, ns := range []string{"binding:", "credential:", "provider:", "meta:"} {
		if strings.HasPrefix(key, prefix+ns) {
			return excluded(key, "non-target namespace "+ns)
		}
	}
	if strings.HasPrefix(key, prefix+"node:") {
		return classifyNodeKey(prefix, key)
	}
	if strings.HasPrefix(key, prefix+"win:") {
		return classifyWindowKey(prefix, key)
	}
	if strings.HasPrefix(key, prefix+"idx:model:") {
		// The candidate index is not copied from legacy state: it is
		// rebuilt from provider/config authority and separately verified
		// (doc 14 §2, §5.2.6). It leaves the migratable path entirely.
		return excluded(key, "candidate index is rebuilt, not copied")
	}
	return excluded(key, "non-target key")
}

func classifyNodeKey(prefix, key string) ClassifiedKey {
	if strings.HasPrefix(key, prefix+"node:k2:") {
		if p, ok := store.ParseNodeKeyAny(prefix, key); ok && p.Schema == store.KeySchemaK2 {
			t := p.ParsedNodeKey
			return ClassifiedKey{SourceKey: key, Class: ClassCanonicalPresent, Reason: "canonical node key", Schema: store.KeySchemaK2, Tuple: &t}
		}
		return ambiguous(key, "malformed canonical node key")
	}
	parts := strings.Split(strings.TrimPrefix(key, prefix+"node:"), ":")
	var tenant, cidStr, raw string
	switch {
	case len(parts) == 4 && parts[0] == "t" && isAllDigits(parts[1]) && parts[1] != "":
		tenant, cidStr, raw = parts[1], parts[2], parts[3]
	case len(parts) == 3 && parts[0] != "" && parts[0] != "t" && !isAllDigits(parts[0]):
		tenant, cidStr, raw = parts[0], parts[1], parts[2]
	case len(parts) == 2:
		tenant, cidStr, raw = "", parts[0], parts[1]
	default:
		return ambiguous(key, "segment count matches no legacy grammar form exactly")
	}
	if cid := parsePositiveDecimal(cidStr); cid <= 0 {
		return ambiguous(key, "credential segment is not a positive decimal")
	}
	if tenant == "" {
		return ambiguous(key, "empty tenant has no canonical representation (operator mapping required)")
	}
	cid, _ := strconv.Atoi(cidStr)
	if store.NodeKeyForTenant(prefix, tenant, cid, raw) != key {
		return ambiguous(key, "tuple does not round-trip to the exact source bytes")
	}
	return ClassifiedKey{
		SourceKey: key,
		Class:     ClassMigratable,
		Reason:    "unique tuple, strict segments, exact round-trip",
		Schema:    store.KeySchemaLegacy,
		Tuple:     &store.ParsedNodeKey{TenantID: tenant, CredentialID: cid, RawModel: raw},
	}
}

func classifyWindowKey(prefix, key string) ClassifiedKey {
	if strings.HasPrefix(key, prefix+"win:k2:") {
		return ClassifiedKey{SourceKey: key, Class: ClassCanonicalPresent, Reason: "canonical window key", Schema: store.KeySchemaK2}
	}
	rest := strings.TrimPrefix(key, prefix+"win:")
	bucket := rest
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		bucket = rest[:i]
	} else {
		return ambiguous(key, "window key has no bucket segment")
	}
	switch bucket {
	case "1m", "5m", "30m":
	default:
		return ambiguous(key, "window bucket is not one of 1m/5m/30m")
	}
	parts := strings.Split(strings.TrimPrefix(rest, bucket+":"), ":")
	var tenant, cidStr, raw string
	switch {
	case len(parts) == 4 && parts[0] == "t" && isAllDigits(parts[1]) && parts[1] != "":
		tenant, cidStr, raw = parts[1], parts[2], parts[3]
	case len(parts) == 3 && parts[0] != "" && parts[0] != "t" && !isAllDigits(parts[0]):
		tenant, cidStr, raw = parts[0], parts[1], parts[2]
	default:
		return ambiguous(key, "window segment count matches no legacy grammar form exactly")
	}
	if parsePositiveDecimal(cidStr) <= 0 {
		return ambiguous(key, "window credential segment is not a positive decimal")
	}
	if tenant == "" {
		return ambiguous(key, "window key has empty tenant (operator mapping required)")
	}
	cid, _ := strconv.Atoi(cidStr)
	if store.WindowKeyForTenant(prefix, tenant, cid, raw, bucket) != key {
		return ambiguous(key, "window tuple does not round-trip to the exact source bytes")
	}
	return ClassifiedKey{
		SourceKey: key,
		Class:     ClassMigratable,
		Reason:    "unique window tuple, strict segments, exact round-trip",
		Schema:    store.KeySchemaLegacy,
		Tuple:     &store.ParsedNodeKey{TenantID: tenant, CredentialID: cid, RawModel: raw},
	}
}

func excluded(key, reason string) ClassifiedKey {
	return ClassifiedKey{SourceKey: key, Class: ClassExcludedNonAuthoritative, Reason: reason}
}

func ambiguous(key, reason string) ClassifiedKey {
	return ClassifiedKey{SourceKey: key, Class: ClassAmbiguous, Reason: reason}
}

func isAllDigits(s string) bool {
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

func parsePositiveDecimal(s string) int {
	if !isAllDigits(s) || (len(s) > 1 && s[0] == '0') {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}
