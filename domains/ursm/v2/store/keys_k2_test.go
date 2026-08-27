package store

import (
	"fmt"
	"math/rand"
	"testing"
)

// Golden bytes for the k2 canonical schema (doc 14 §2) are derived
// independently of the implementation (python3 base64.urlsafe_b64encode,
// padding stripped) from the grammar:
//
//	<prefix>node:k2:<b64url-tenant>:<credential-id>:<b64url-raw-model>
//	<prefix>win:k2:<bucket>:<b64url-tenant>:<credential-id>:<b64url-raw-model>
//	<prefix>idx:model:k2:<b64url-tenant>:<b64url-canonical>:<b64url-profile>:<b64url-modality>

func TestCanonicalNodeKeyK2GoldenBytes(t *testing.T) {
	got, err := K2NodeKeyForTenant("ursm:v2:", "tenant-a", 7, "model-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "ursm:v2:node:k2:dGVuYW50LWE:7:bW9kZWwtYQ"; got != want {
		t.Fatalf("k2 node key = %q, want %q", got, want)
	}
}

func TestCanonicalWindowKeyK2GoldenBytes(t *testing.T) {
	got, err := K2WindowKeyForTenant("ursm:v2:", "tenant-a", 7, "model-a", "5m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "ursm:v2:win:k2:5m:dGVuYW50LWE:7:bW9kZWwtYQ"; got != want {
		t.Fatalf("k2 window key = %q, want %q", got, want)
	}
}

func TestCanonicalCandidateIndexKeyK2GoldenBytes(t *testing.T) {
	got, err := K2CandidateIndexKey("ursm:v2:", "tenant-a", "model-a", "profile-x", "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "ursm:v2:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ:cHJvZmlsZS14:dGV4dA"; got != want {
		t.Fatalf("k2 candidate index key = %q, want %q", got, want)
	}
}

func TestCanonicalKeysRejectEmptyAndInvalidInputs(t *testing.T) {
	if _, err := K2NodeKeyForTenant("ursm:v2:", "", 7, "model-a"); err == nil {
		t.Fatal("empty tenant must be rejected: canonical never represents it (doc 14 §2)")
	}
	if _, err := K2NodeKeyForTenant("ursm:v2:", "tenant-a", 0, "model-a"); err == nil {
		t.Fatal("credential id 0 must be rejected")
	}
	if _, err := K2NodeKeyForTenant("ursm:v2:", "tenant-a", -1, "model-a"); err == nil {
		t.Fatal("negative credential id must be rejected")
	}
	if _, err := K2NodeKeyForTenant("ursm:v2:", "tenant-a", 7, ""); err == nil {
		t.Fatal("empty raw model must be rejected")
	}
	for _, bucket := range []string{"", "2m", "1h", "1m:extra"} {
		if _, err := K2WindowKeyForTenant("ursm:v2:", "tenant-a", 7, "model-a", bucket); err == nil {
			t.Fatalf("bucket %q must be rejected: buckets are frozen to 1m/5m/30m", bucket)
		}
	}
	if _, err := K2WindowKeyForTenant("ursm:v2:", "", 7, "model-a", "5m"); err == nil {
		t.Fatal("empty tenant must be rejected for windows too")
	}
	if _, err := K2CandidateIndexKey("ursm:v2:", "", "model-a", "profile-x", "text"); err == nil {
		t.Fatal("empty tenant must be rejected for candidate index")
	}
	if _, err := K2CandidateIndexKey("ursm:v2:", "tenant-a", "", "profile-x", "text"); err == nil {
		t.Fatal("empty canonical model must be rejected")
	}
	if _, err := K2CandidateIndexKey("ursm:v2:", "tenant-a", "model-a", "", "text"); err == nil {
		t.Fatal("empty profile must be rejected")
	}
	if _, err := K2CandidateIndexKey("ursm:v2:", "tenant-a", "model-a", "profile-x", ""); err == nil {
		t.Fatal("empty modality must be rejected")
	}
}

// TestNodeKeySetForTenantRetainsTuple: the key set keeps the tuple it was
// built from so dual/canonical store paths can derive the k2 set without
// re-parsing legacy keys — a delimiter-bearing tuple cannot survive that
// round-trip, and the write path always starts from the true tuple.
func TestNodeKeySetForTenantRetainsTuple(t *testing.T) {
	set := NodeKeySetForTenant("p:", "a:7:b", 8, "c")
	if set.TenantID != "a:7:b" || set.CredentialID != 8 || set.RawModel != "c" {
		t.Fatalf("tuple = (%q,%d,%q), want (a:7:b,8,c)", set.TenantID, set.CredentialID, set.RawModel)
	}
}

func TestParseNodeKeyAnyRoundTripAndSchemaOrigin(t *testing.T) {
	// k2 round-trip: a constructed canonical key parses back to the exact
	// tuple and reports the k2 schema origin.
	k2Key, err := K2NodeKeyForTenant("ursm:v2:", "tenant-a", 7, "model:with:colon")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p, ok := ParseNodeKeyAny("ursm:v2:", k2Key)
	if !ok || p.Schema != KeySchemaK2 {
		t.Fatalf("k2 parse ok=%v schema=%v", ok, p.Schema)
	}
	if p.TenantID != "tenant-a" || p.CredentialID != 7 || p.RawModel != "model:with:colon" {
		t.Fatalf("k2 tuple = %+v", p.ParsedNodeKey)
	}

	// legacy keys still parse through the same entry point with the legacy
	// schema origin, so callers cannot treat a parse success as schema-free.
	p, ok = ParseNodeKeyAny("ursm:v2:", "ursm:v2:node:tenant-a:34:model:with:colon")
	if !ok || p.Schema != KeySchemaLegacy {
		t.Fatalf("legacy parse ok=%v schema=%v", ok, p.Schema)
	}
	if p.TenantID != "tenant-a" || p.CredentialID != 34 || p.RawModel != "model:with:colon" {
		t.Fatalf("legacy tuple = %+v", p.ParsedNodeKey)
	}
	p, ok = ParseNodeKeyAny("ursm:v2:", NodeKey("ursm:v2:", 12, "gpt-4"))
	if !ok || p.Schema != KeySchemaLegacy || p.TenantID != "" || p.CredentialID != 12 || p.RawModel != "gpt-4" {
		t.Fatalf("legacy non-tenant parse = %+v ok=%v", p.ParsedNodeKey, ok)
	}
}

func TestParseNodeKeyAnyStrictRejection(t *testing.T) {
	bad := []string{
		"ursm:v2:node:k2:",              // empty remainder
		"ursm:v2:node:k2:YQ:7",          // too few segments
		"ursm:v2:node:k2:YQ:7:bQ:extra", // too many segments — no rejoin, ever
		"ursm:v2:node:k2:!bad:7:bQ",     // invalid base64url tenant
		"ursm:v2:node:k2:YQ:7:Yg==",     // padded encoding is not raw
		"ursm:v2:node:k2:YQ:7:YR",       // non-canonical trailing bits (YR re-encodes to YQ)
		"ursm:v2:node:k2:YQ:0:bQ",       // credential id 0
		"ursm:v2:node:k2:YQ:-1:bQ",      // signed credential id
		"ursm:v2:node:k2:YQ:+7:bQ",      // explicit plus sign
		"ursm:v2:node:k2:YQ:07:bQ",      // leading zero is not canonical decimal
		"ursm:v2:node:k2::7:bQ",         // empty tenant segment — k2 never represents empty tenant
		"ursm:v2:node:k2:YQ:7:",         // empty raw model segment
	}
	for _, k := range bad {
		if _, ok := ParseNodeKeyAny("ursm:v2:", k); ok {
			t.Fatalf("strict k2 parser must reject %q", k)
		}
	}
	// Non-node keys never parse through the node entry point.
	for _, k := range []string{"ursm:v2:win:1m:12:gpt-4", "ursm:v2:meta:ready", "ursm:v2:idx:model:tenant-a:model-a:chat:text"} {
		if _, ok := ParseNodeKeyAny("ursm:v2:", k); ok {
			t.Fatalf("non-node key %q must not parse", k)
		}
	}
}

// TestParseNodeKeyRejectsK2MarkerKeys guards the loose legacy parser against
// mis-parsing canonical keys as legacy tuples. The dangerous case is an
// all-digit base64url tenant segment: b64("\xd3\x5d\xb7") == "0123", which
// the untagged legacy branch reads as {tenant:"k2", cid:123} with ok=true
// (verified empirically before this fix). Every k2 key must be rejected
// here so persist/coverage cannot consume a wrong tuple.
func TestParseNodeKeyRejectsK2MarkerKeys(t *testing.T) {
	allDigitB64Tenant := "ursm:v2:node:k2:0123:7:bW9kZWwtYQ"
	if _, ok := ParseNodeKey("ursm:v2:", allDigitB64Tenant); ok {
		t.Fatalf("legacy parser must reject all-digit-b64 k2 key %q", allDigitB64Tenant)
	}
	normal, err := K2NodeKeyForTenant("ursm:v2:", "tenant-a", 7, "model-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := ParseNodeKey("ursm:v2:", normal); ok {
		t.Fatalf("legacy parser must reject k2 key %q", normal)
	}
	if _, ok := ParseNodeKeyAny("ursm:v2:", allDigitB64Tenant); !ok {
		t.Fatal("the any-schema parser must still parse the all-digit-b64 k2 key correctly")
	}
}

func TestCanonicalKeysRepresentDelimiterBearingComponents(t *testing.T) {
	cases := []struct {
		tenant string
		cid    int
		raw    string
	}{
		{"a", 7, "b:8:c"},
		{"a:7:b", 8, "c"},
		{"123", 34, "model:with:colon"},
		{":leading", 5, ":model:"},
		{"double::colon", 5, "a::b"},
		{"租户:一", 5, "模型:测试"},
		{"café", 5, "café:model"},
		{"trail ", 5, " space model "},
	}
	for _, tc := range cases {
		k, err := K2NodeKeyForTenant("ursm:v2:", tc.tenant, tc.cid, tc.raw)
		if err != nil {
			t.Fatalf("tenant=%q raw=%q: unexpected error: %v", tc.tenant, tc.raw, err)
		}
		p, ok := ParseNodeKeyAny("ursm:v2:", k)
		if !ok || p.Schema != KeySchemaK2 {
			t.Fatalf("tenant=%q raw=%q: parse ok=%v schema=%v key=%q", tc.tenant, tc.raw, ok, p.Schema, k)
		}
		if p.TenantID != tc.tenant || p.CredentialID != tc.cid || p.RawModel != tc.raw {
			t.Fatalf("tenant=%q raw=%q: round-trip got %+v from key %q", tc.tenant, tc.raw, p.ParsedNodeKey, k)
		}
	}
}

// TestCanonicalKeyCollisionProperty pins the frozen historical collision
// pair (doc 14 §1) and sweeps a representative delimiter/Unicode corpus:
// every distinct tuple must yield a distinct key and round-trip losslessly.
func TestCanonicalKeyCollisionProperty(t *testing.T) {
	// The two tuples that collide in the legacy grammar
	// (both produce contract:node:a:7:b:8:c) must diverge under k2.
	a, err := K2NodeKeyForTenant("contract:", "a", 7, "b:8:c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := K2NodeKeyForTenant("contract:", "a:7:b", 8, "c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "contract:node:k2:YQ:7:Yjo4OmM"; a != want {
		t.Fatalf("frozen pair a = %q, want %q", a, want)
	}
	if want := "contract:node:k2:YTo3OmI:8:Yw"; b != want {
		t.Fatalf("frozen pair b = %q, want %q", b, want)
	}
	if a == b {
		t.Fatal("frozen legacy collision pair must not collide in k2")
	}

	type tup struct {
		tenant string
		cid    int
		raw    string
	}
	tenants := []string{"a", "a:7:b", "a:7", "123", ":t:", "t:", "租户", "café", "a:b:c:d:e"}
	raws := []string{"m", "b:8:c", ":", "::", ":m:", "模型", "b:8", "m:1:2:3"}
	seen := make(map[string]tup)
	for _, tenant := range tenants {
		for cid := 1; cid <= 3; cid++ {
			for _, raw := range raws {
				k, err := K2NodeKeyForTenant("p:", tenant, cid, raw)
				if err != nil {
					t.Fatalf("tenant=%q cid=%d raw=%q: unexpected error: %v", tenant, cid, raw, err)
				}
				cur := tup{tenant, cid, raw}
				if prev, dup := seen[k]; dup {
					t.Fatalf("collision on key %q between %+v and %+v", k, prev, cur)
				}
				seen[k] = cur
				p, ok := ParseNodeKeyAny("p:", k)
				if !ok || p.TenantID != tenant || p.CredentialID != cid || p.RawModel != raw {
					t.Fatalf("round-trip failed for %+v on key %q", cur, k)
				}
			}
		}
	}
}

// Strict window/candidate parsers share the same k2 marker and frozen
// bucket vocabulary as the constructors; the parser-only path was
// contributed by the parallel main-branch session and must stay consistent
// with the production entry points (no rejoin, no re-canonicalisation).
func TestParseWindowKeyAnyRoundTrip(t *testing.T) {
	for _, bucket := range []string{"1m", "5m", "30m"} {
		k, err := K2WindowKeyForTenant("ursm:v2:", "tenant-a", 7, "model:a", bucket)
		if err != nil {
			t.Fatalf("bucket=%s: unexpected error: %v", bucket, err)
		}
		p, ok := ParseWindowKeyAny("ursm:v2:", k)
		if !ok || p.Bucket != bucket || p.TenantID != "tenant-a" || p.CredentialID != 7 || p.RawModel != "model:a" {
			t.Fatalf("bucket=%s parse = %+v ok=%v", bucket, p, ok)
		}
	}
	for _, bucket := range []string{"", "2m", "1h", "1m:extra"} {
		k, _ := K2WindowKeyForTenant("ursm:v2:", "tenant-a", 7, "model:a", bucket)
		if k == "" {
			continue
		}
		if _, ok := ParseWindowKeyAny("ursm:v2:", k); ok {
			t.Fatalf("unknown bucket must not parse: %s", k)
		}
	}
	if _, ok := ParseWindowKeyAny("ursm:v2:", "ursm:v2:win:1m:tenant-a:7:model"); ok {
		t.Fatal("legacy window key must not parse as k2")
	}
}

// Reserved-namespace strictness — these cases are valid under the permissive
// legacy parser but must fail closed under the canonical strict parser.
// Contributed by the parallel main-branch session; preserved here so the
// two parsers stay consistent on every reserved-namespace edge case.
func TestParseNodeKeyCanonicalRejectsMalformedKeys(t *testing.T) {
	const prefix = "ursm:v2:"
	rejected := map[string]string{
		"empty tenant segment":    "ursm:v2:node:k2::7:Z3B0LTQ",
		"empty raw model segment": "ursm:v2:node:k2:dGVuYW50LWE:7:",
		"credential zero":         "ursm:v2:node:k2:dGVuYW50LWE:0:Z3B0LTQ",
		"negative credential":     "ursm:v2:node:k2:dGVuYW50LWE:-7:Z3B0LTQ",
		"leading-zero credential": "ursm:v2:node:k2:dGVuYW50LWE:07:Z3B0LTQ",
		"signed credential":       "ursm:v2:node:k2:dGVuYW50LWE:+7:Z3B0LTQ",
		"non-decimal credential":  "ursm:v2:node:k2:dGVuYW50LWE:7x:Z3B0LTQ",
		"missing raw segment":     "ursm:v2:node:k2:dGVuYW50LWE:7",
		"extra segment":           "ursm:v2:node:k2:dGVuYW50LWE:7:Z3B0LTQ:YQ",
		"standard alphabet plus":  "ursm:v2:node:k2:a+b:7:Z3B0LTQ",
		"standard alphabet slash": "ursm:v2:node:k2:a/b:7:Z3B0LTQ",
		"padded segment":          "ursm:v2:node:k2:YQ=:7:Z3B0LTQ",
		"non-canonical tail bits": "ursm:v2:node:k2:QR:7:Z3B0LTQ",
		"wrong prefix":            "other:node:k2:dGVuYW50LWE:7:Z3B0LTQ",
		"missing k2 marker":       "ursm:v2:node:k3:dGVuYW50LWE:7:Z3B0LTQ",
		"window key against node": "ursm:v2:win:k2:1m:dGVuYW50LWE:7:Z3B0LTQ",
		"unparseable legacy":      "ursm:v2:node:xx:not-a-number",
		// Valid under the permissive legacy parser (tenant k2, credential
		// 1234, raw 07:YQ), but the reserved k2 namespace must fail closed
		// when its canonical credential is non-canonical.
		"malformed reserved k2 fallback": "ursm:v2:node:k2:1234:07:YQ",
	}
	for reason, key := range rejected {
		if parsed, schema, ok := ParseNodeKeyCanonical(prefix, key); ok {
			t.Fatalf("%s (%q) must not parse, got %+v schema=%v", reason, key, parsed, schema)
		}
	}
}

// ParseNodeKeyCanonical reports SchemaLegacy for every legacy grammar
// (non-tenant, tagged numeric tenant, untagged string tenant) and SchemaK2
// for canonical keys. A key matching both grammars resolves as k2: once
// the k2 marker exists it owns the node:k2: namespace.
func TestParseNodeKeyCanonicalReportsSchemaSource(t *testing.T) {
	const prefix = "ursm:v2:"
	legacy := map[string]string{
		"ursm:v2:node:12:gpt-4":                     "",
		"ursm:v2:node:t:123:34:model:with:colon":    "123",
		"ursm:v2:node:tenant-a:34:model:with:colon": "tenant-a",
	}
	for key, wantTenant := range legacy {
		parsed, schema, ok := ParseNodeKeyCanonical(prefix, key)
		if !ok || schema != SchemaLegacy || parsed.TenantID != wantTenant {
			t.Fatalf("legacy source %q = %+v schema=%v ok=%v", key, parsed, schema, ok)
		}
	}
	k2Key := NodeKeyCanonical(prefix, "tenant-a", 7, "gpt-4")
	parsed, schema, ok := ParseNodeKeyCanonical(prefix, k2Key)
	if !ok || schema != SchemaK2 || parsed.TenantID != "tenant-a" || parsed.CredentialID != 7 || parsed.RawModel != "gpt-4" {
		t.Fatalf("k2 source = %+v schema=%v ok=%v", parsed, schema, ok)
	}
	if _, _, ok := ParseNodeKeyCanonical(prefix, "ursm:v2:win:k2:1m:dGVuYW50LWE:7:Z3B0LTQ"); ok {
		t.Fatal("window key must not report a node schema")
	}
	// "node:k2:1234:7:YQ" under the strict legacy parser (which refuses
	// the reserved "k2" marker) does not parse; under the canonical
	// parser it resolves as k2 because the marker is present. This is the
	// exact behaviour doc 14 §2 / doc 16 slice 4 require: when the
	// reserved k2 namespace is detected the legacy grammar yields the
	// floor to canonical, and a malformed canonical tuple is rejected.
	dual := "ursm:v2:node:k2:1234:7:YQ"
	if _, ok := ParseNodeKey(prefix, dual); ok {
		t.Fatalf("strict legacy parser must reject the reserved k2 marker")
	}
	parsed, schema, ok = ParseNodeKeyCanonical(prefix, dual)
	if !ok || schema != SchemaK2 || parsed.CredentialID != 7 || parsed.RawModel != "a" {
		t.Fatalf("dual-shape key must resolve as k2, got %+v schema=%v ok=%v", parsed, schema, ok)
	}
}

func TestParseCandidateIndexKeyAnyRoundTrip(t *testing.T) {
	k, err := K2CandidateIndexKey("ursm:v2:", "tenant-a", "model:a", "chat", "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p, ok := ParseCandidateIndexKeyAny("ursm:v2:", k)
	if !ok || p.TenantID != "tenant-a" || p.CanonicalModel != "model:a" || p.Profile != "chat" || p.Modality != "text" {
		t.Fatalf("index parse = %+v ok=%v", p, ok)
	}
	if _, ok := ParseCandidateIndexKeyAny("ursm:v2:", "ursm:v2:idx:model:tenant-a:model-a:chat:text"); ok {
		t.Fatal("legacy index key must not parse as k2")
	}
}

// Deterministic corpus of delimiter-heavy, numeric, Unicode, leading/
// trailing colon and repeated colon cases that the parser must classify
// exactly (doc 14 §7, doc 16 slice 5). The property test above adds a
// probabilistic check; this table freezes the edge cases it cannot prove.
func TestCanonicalDeterministicDelimiterCorpusRoundTrip(t *testing.T) {
	const prefix = "ursm:v2:"
	for _, raw := range []string{
		"",
		":",
		"::",
		"a:",
		":a",
		":a:",
		"a::b",
		"租户-α",
		"gpt-4.1-turbo",
		"with:colon",
		"trailing:",
		":leading",
		"::leading",
		"trailing::",
		"model with space",
	} {
		tenant := "t-" + raw
		node := NodeKeyCanonical(prefix, tenant, 7, raw)
		parsed, schema, ok := ParseNodeKeyCanonical(prefix, node)
		if raw == "" {
			if ok {
				t.Fatalf("empty raw must not round-trip, got %+v schema=%v", parsed, schema)
			}
			continue
		}
		if !ok || schema != SchemaK2 || parsed.TenantID != tenant || parsed.CredentialID != 7 || parsed.RawModel != raw {
			t.Fatalf("raw=%q node round-trip = %+v schema=%v ok=%v", raw, parsed, schema, ok)
		}
		parsedW, okW := ParseWindowKeyCanonical(prefix, WindowKeyCanonical(prefix, tenant, 7, raw, "5m"))
		if !okW || parsedW.TenantID != tenant || parsedW.CredentialID != 7 || parsedW.RawModel != raw || parsedW.Bucket != "5m" {
			t.Fatalf("raw=%q window round-trip = %+v ok=%v", raw, parsedW, okW)
		}
		parsedI, okI := ParseCandidateIndexKeyCanonical(prefix, CandidateIndexKeyCanonical(prefix, tenant, raw, "chat", "text"))
		if !okI || parsedI.TenantID != tenant || parsedI.CanonicalModel != raw || parsedI.Profile != "chat" || parsedI.Modality != "text" {
			t.Fatalf("raw=%q index round-trip = %+v ok=%v", raw, parsedI, okI)
		}
	}
}

// Every parser must reject the same strictness cases the node parser
// rejects. Doc 14 §2 / doc 16 slice 4 require exact segment count,
// canonical base64url segments, positive decimal credential and frozen
// bucket whitelist.
func TestCanonicalWindowAndIndexParsersRejectStrictnessViolations(t *testing.T) {
	const prefix = "ursm:v2:"
	rejected := []string{
		// Empty tenant / model segments
		"ursm:v2:win:k2:5m::7:Z3B0LTQ",
		"ursm:v2:win:k2:5m:dGVuYW50LWE:7:",
		// Invalid base64url alphabet
		"ursm:v2:win:k2:5m:a+b:7:Z3B0LTQ",
		"ursm:v2:win:k2:5m:a/b:7:Z3B0LTQ",
		// Padded segment
		"ursm:v2:win:k2:5m:YQ=:7:Z3B0LTQ",
		// Non-canonical trailing bits
		"ursm:v2:win:k2:5m:QR:7:Z3B0LTQ",
		// Credential variants (must be a positive decimal)
		"ursm:v2:win:k2:5m:dGVuYW50LWE:0:Z3B0LTQ",
		"ursm:v2:win:k2:5m:dGVuYW50LWE:-7:Z3B0LTQ",
		"ursm:v2:win:k2:5m:dGVuYW50LWE:07:Z3B0LTQ",
		"ursm:v2:win:k2:5m:dGVuYW50LWE:+7:Z3B0LTQ",
		"ursm:v2:win:k2:5m:dGVuYW50LWE:7x:Z3B0LTQ",
		// Missing/extra segments
		"ursm:v2:win:k2:5m:dGVuYW50LWE:7",
		"ursm:v2:win:k2:5m:dGVuYW50LWE:7:Z3B0LTQ:YQ",
		// Unknown bucket
		"ursm:v2:win:k2:2m:dGVuYW50LWE:7:Z3B0LTQ",
		// Wrong prefix / marker
		"other:win:k2:5m:dGVuYW50LWE:7:Z3B0LTQ",
		"ursm:v2:win:k3:5m:dGVuYW50LWE:7:Z3B0LTQ",
		// Node keys fed to the window parser
		"ursm:v2:node:k2:dGVuYW50LWE:7:Z3B0LTQ",
	}
	for _, key := range rejected {
		if _, ok := ParseWindowKeyCanonical(prefix, key); ok {
			t.Fatalf("window parser must reject %q", key)
		}
	}
	indexRejected := []string{
		"ursm:v2:idx:model:k2::bW9kZWwtYQ:Y2hhdA:dGV4dA",
		"ursm:v2:idx:model:k2:dGVuYW50LWE::Y2hhdA:dGV4dA",
		"ursm:v2:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ::dGV4dA",
		"ursm:v2:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ:Y2hhdA:",
		"ursm:v2:idx:model:k2:a+b:bW9kZWwtYQ:Y2hhdA:dGV4dA",
		"ursm:v2:idx:model:k2:YQ=:bW9kZWwtYQ:Y2hhdA:dGV4dA",
		"ursm:v2:idx:model:k2:QR:bW9kZWwtYQ:Y2hhdA:dGV4dA",
		"ursm:v2:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ:Y2hhdA:dGV4dA:extra",
		"ursm:v2:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ:Y2hhdA",
		"other:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ:Y2hhdA:dGV4dA",
		"ursm:v2:idx:model:k3:dGVuYW50LWE:bW9kZWwtYQ:Y2hhdA:dGV4dA",
		"ursm:v2:node:k2:dGVuYW50LWE:7:Z3B0LTQ",
	}
	for _, key := range indexRejected {
		if _, ok := ParseCandidateIndexKeyCanonical(prefix, key); ok {
			t.Fatalf("index parser must reject %q", key)
		}
	}
}

// Property: over an adversarial corpus (delimiter-heavy, numeric, Unicode,
// leading/trailing colons) canonical encoding is injective — equal keys
// imply equal tuples — and every constructed key round-trips exactly.
func TestCanonicalKeyCollisionPropertyOverGeneratedCorpus(t *testing.T) {
	const prefix = "ursm:v2:"
	rng := rand.New(rand.NewSource(1))
	alphabet := []rune("ab:0129租户-αxX")
	gen := func(maxLen int) string {
		out := make([]rune, 1+rng.Intn(maxLen))
		for i := range out {
			out[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(out)
	}
	seenNode := map[string]string{}
	for i := 0; i < 200; i++ {
		tenant, raw := gen(6), gen(9)
		cid := 1 + rng.Intn(100000)
		tupleID := fmt.Sprintf("%q/%d/%q", tenant, cid, raw)

		nodeKey, err := K2NodeKeyForTenant(prefix, tenant, cid, raw)
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
		if prev, dup := seenNode[nodeKey]; dup && prev != tupleID {
			t.Fatalf("node key collision: %s vs %s both encode to %s", prev, tupleID, nodeKey)
		}
		seenNode[nodeKey] = tupleID
		p, ok := ParseNodeKeyAny(prefix, nodeKey)
		if !ok || p.TenantID != tenant || p.CredentialID != cid || p.RawModel != raw {
			t.Fatalf("iter %d node round-trip = %+v ok=%v", i, p, ok)
		}
	}
}