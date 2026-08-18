package store

import "testing"

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

	type tuple struct {
		tenant string
		cid    int
		raw    string
	}
	tenants := []string{"a", "a:7:b", "a:7", "123", ":t:", "t:", "租户", "café", "a:b:c:d:e"}
	raws := []string{"m", "b:8:c", ":", "::", ":m:", "模型", "b:8", "m:1:2:3"}
	seen := make(map[string]tuple)
	for _, tenant := range tenants {
		for cid := 1; cid <= 3; cid++ {
			for _, raw := range raws {
				k, err := K2NodeKeyForTenant("p:", tenant, cid, raw)
				if err != nil {
					t.Fatalf("tenant=%q cid=%d raw=%q: unexpected error: %v", tenant, cid, raw, err)
				}
				cur := tuple{tenant, cid, raw}
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
