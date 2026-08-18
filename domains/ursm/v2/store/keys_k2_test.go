package store

import (
	"fmt"
	"math/rand"
	"testing"
)

// Golden bytes for the k2 canonical grammar frozen in doc 14 §2 and pinned
// by doc 16 §2. base64url(raw): "tenant-a"=dGVuYW50LWE, "gpt-4"=Z3B0LTQ,
// "model:with:colon"=bW9kZWw6d2l0aDpjb2xvbg, "a"=YQ, "b:8:c"=Yjo4OmM,
// "a:7:b"=YTo3OmI, "c"=Yw, "123"=MTIz, "7"=Nw, "m"=bQ, "租户-α"=56ef5oi3Lc6x,
// "model-a"=bW9kZWwtYQ, "chat"=Y2hhdA, "text"=dGV4dA, "x"=eA, "y:z"=eTp6, "z"=eg.

// Doc 16 §2 slice 1: canonical node key golden bytes + round-trip + the
// historical collision pair frozen in doc 14 §1.
func TestCanonicalNodeKeyK2RoundTripAndFrozenCollisionPair(t *testing.T) {
	const prefix = "contract:"
	// The frozen collision pair: two tuples that collapse onto one legacy key.
	a := NodeKeyCanonical(prefix, "a", 7, "b:8:c") // a→YQ  b:8:c→Yjo4OmM
	b := NodeKeyCanonical(prefix, "a:7:b", 8, "c") // a:7:b→YTo3OmI  c→Yw
	if want := "contract:node:k2:YQ:7:Yjo4OmM"; a != want {
		t.Fatalf("canonical a = %q, want %q", a, want)
	}
	if want := "contract:node:k2:YTo3OmI:8:Yw"; b != want {
		t.Fatalf("canonical b = %q, want %q", b, want)
	}
	if a == b {
		t.Fatal("frozen legacy collision pair must not collide in k2")
	}
	pa, sa, ok := ParseNodeKeyCanonical(prefix, a) // strict parser must report schema source
	if !ok || sa != SchemaK2 || pa.TenantID != "a" || pa.CredentialID != 7 || pa.RawModel != "b:8:c" {
		t.Fatalf("k2 parse = %+v schema=%v ok=%v", pa, sa, ok)
	}
	pb, sb, ok := ParseNodeKeyCanonical(prefix, b)
	if !ok || sb != SchemaK2 || pb.TenantID != "a:7:b" || pb.CredentialID != 8 || pb.RawModel != "c" {
		t.Fatalf("k2 parse = %+v schema=%v ok=%v", pb, sb, ok)
	}
	// Numeric tenants need no legacy t-tag in k2 — b64url is unambiguous.
	if got := NodeKeyCanonical(prefix, "7", 7, "m"); got != "contract:node:k2:Nw:7:bQ" {
		t.Fatalf("numeric tenant canonical = %q", got) // 7→Nw  m→bQ
	}
	if parsed, schema, ok := ParseNodeKeyCanonical(prefix, NodeKeyCanonical(prefix, "7", 7, "m")); !ok || schema != SchemaK2 || parsed.TenantID != "7" || parsed.RawModel != "m" {
		t.Fatalf("numeric tenant round-trip = %+v schema=%v ok=%v", parsed, schema, ok)
	}
	if NodeKeyForTenant(prefix, "a", 7, "b:8:c") != NodeKeyForTenant(prefix, "a:7:b", 8, "c") {
		t.Fatal("precondition: the pair must still collide in legacy")
	}
}

func TestCanonicalNodeKeyUnicodeGoldenAndRoundTrip(t *testing.T) {
	const prefix = "ursm:v2:"
	got := NodeKeyCanonical(prefix, "租户-α", 12, "model:with:colon")
	if want := "ursm:v2:node:k2:56ef5oi3Lc6x:12:bW9kZWw6d2l0aDpjb2xvbg"; got != want {
		t.Fatalf("unicode key = %q, want %q", got, want)
	}
	parsed, schema, ok := ParseNodeKeyCanonical(prefix, got)
	if !ok || schema != SchemaK2 || parsed.TenantID != "租户-α" || parsed.CredentialID != 12 || parsed.RawModel != "model:with:colon" {
		t.Fatalf("unicode round-trip = %+v schema=%v ok=%v", parsed, schema, ok)
	}
}

// Unrepresentable inputs encode to keys that no parser accepts: the
// canonical grammar cannot smuggle an empty tenant or raw model past
// decoding, and non-positive credentials never parse.
func TestCanonicalConstructionUnrepresentableInputsFailAtParse(t *testing.T) {
	const prefix = "ursm:v2:"
	for name, key := range map[string]string{
		"empty tenant":          NodeKeyCanonical(prefix, "", 7, "gpt-4"),
		"empty raw model":       NodeKeyCanonical(prefix, "tenant-a", 7, ""),
		"credential zero":       NodeKeyCanonical(prefix, "tenant-a", 0, "gpt-4"),
		"negative credential":   NodeKeyCanonical(prefix, "tenant-a", -3, "gpt-4"),
		"window unknown bucket": WindowKeyCanonical(prefix, "tenant-a", 7, "gpt-4", "2m"),
		"window empty tenant":   WindowKeyCanonical(prefix, "", 7, "gpt-4", "1m"),
		"index empty tenant":    CandidateIndexKeyCanonical(prefix, "", "model-a", "chat", "text"),
		"index empty model":     CandidateIndexKeyCanonical(prefix, "tenant-a", "", "chat", "text"),
		"index empty profile":   CandidateIndexKeyCanonical(prefix, "tenant-a", "model-a", "", "text"),
		"index empty modality":  CandidateIndexKeyCanonical(prefix, "tenant-a", "model-a", "chat", ""),
	} {
		if _, _, ok := ParseNodeKeyCanonical(prefix, key); ok {
			t.Fatalf("%s must not parse as a node key: %q", name, key)
		}
		if _, ok := ParseWindowKeyCanonical(prefix, key); ok {
			t.Fatalf("%s must not parse as a window key: %q", name, key)
		}
		if _, ok := ParseCandidateIndexKeyCanonical(prefix, key); ok {
			t.Fatalf("%s must not parse as an index key: %q", name, key)
		}
	}
}

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
	}
	for reason, key := range rejected {
		if parsed, schema, ok := ParseNodeKeyCanonical(prefix, key); ok {
			t.Fatalf("%s (%q) must not parse, got %+v schema=%v", reason, key, parsed, schema)
		}
	}
}

// ParseNodeKeyCanonical reports SchemaLegacy for every legacy grammar
// (non-tenant, tagged numeric tenant, untagged string tenant) and SchemaK2
// for canonical keys. A key matching both grammars resolves as k2: once the
// k2 marker exists it owns the node:k2: namespace.
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
	// "node:k2:1234:7:YQ" matches both grammars: legacy reads tenant "k2",
	// credential 1234, raw "7:YQ"; k2 reads tenant b64("1234"), credential 7,
	// raw "a". k2 wins.
	dual := "ursm:v2:node:k2:1234:7:YQ"
	if legacyParsed, ok := ParseNodeKey(prefix, dual); !ok || legacyParsed.TenantID != "k2" || legacyParsed.CredentialID != 1234 || legacyParsed.RawModel != "7:YQ" {
		t.Fatalf("dual-shape precondition = %+v ok=%v", legacyParsed, ok)
	}
	parsed, schema, ok = ParseNodeKeyCanonical(prefix, dual)
	if !ok || schema != SchemaK2 || parsed.CredentialID != 7 || parsed.RawModel != "a" {
		t.Fatalf("dual-shape key must resolve as k2, got %+v schema=%v ok=%v", parsed, schema, ok)
	}
}

func TestCanonicalWindowKeyGoldenBucketsAndRoundTrip(t *testing.T) {
	const prefix = "ursm:v2:"
	got := WindowKeyCanonical(prefix, "tenant-a", 7, "gpt-4", "5m")
	if want := "ursm:v2:win:k2:5m:dGVuYW50LWE:7:Z3B0LTQ"; got != want {
		t.Fatalf("canonical window key = %q, want %q", got, want)
	}
	keys := make(map[string]string, 3)
	for _, bucket := range []string{"1m", "5m", "30m"} {
		k := WindowKeyCanonical(prefix, "a:7:b", 8, "c", bucket)
		keys[k] = bucket
		parsed, ok := ParseWindowKeyCanonical(prefix, k)
		if !ok || parsed.Bucket != bucket || parsed.TenantID != "a:7:b" || parsed.CredentialID != 8 || parsed.RawModel != "c" {
			t.Fatalf("bucket %s round-trip = %+v ok=%v", bucket, parsed, ok)
		}
		if bucket == "1m" && k != "ursm:v2:win:k2:1m:YTo3OmI:8:Yw" {
			t.Fatalf("1m golden = %q", k)
		}
	}
	if len(keys) != 3 {
		t.Fatalf("buckets must yield distinct keys, got %d", len(keys))
	}
	if _, ok := ParseWindowKeyCanonical(prefix, WindowKeyCanonical(prefix, "tenant-a", 7, "gpt-4", "2m")); ok {
		t.Fatal("unknown bucket must not parse")
	}
	if _, ok := ParseWindowKeyCanonical(prefix, "ursm:v2:win:1m:tenant-a:7:gpt-4"); ok {
		t.Fatal("legacy window key must not parse as k2")
	}
	if _, ok := ParseWindowKeyCanonical(prefix, NodeKeyCanonical(prefix, "tenant-a", 7, "gpt-4")); ok {
		t.Fatal("node key must not parse as window key")
	}
}

// The legacy index collapses tenant "x:y"/canonical "z" and tenant "x"/
// canonical "y:z" onto one key; k2 must keep them distinct.
func TestCanonicalIndexKeyGoldenCollisionAndRoundTrip(t *testing.T) {
	const prefix = "ursm:v2:"
	got := CandidateIndexKeyCanonical(prefix, "tenant-a", "model-a", "chat", "text")
	if want := "ursm:v2:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ:Y2hhdA:dGV4dA"; got != want {
		t.Fatalf("canonical index key = %q, want %q", got, want)
	}
	if CandidateIndexKey(prefix, "x:y", "z", "chat", "text") != CandidateIndexKey(prefix, "x", "y:z", "chat", "text") {
		t.Fatal("precondition: legacy index keys for the pair must collide")
	}
	a := CandidateIndexKeyCanonical(prefix, "x:y", "z", "chat", "text")
	b := CandidateIndexKeyCanonical(prefix, "x", "y:z", "chat", "text")
	if a == b {
		t.Fatal("index collision pair must not collide in k2")
	}
	pa, ok := ParseCandidateIndexKeyCanonical(prefix, a)
	if !ok || pa.TenantID != "x:y" || pa.CanonicalModel != "z" || pa.Profile != "chat" || pa.Modality != "text" {
		t.Fatalf("round-trip a = %+v ok=%v", pa, ok)
	}
	pb, ok := ParseCandidateIndexKeyCanonical(prefix, b)
	if !ok || pb.TenantID != "x" || pb.CanonicalModel != "y:z" || pb.Profile != "chat" || pb.Modality != "text" {
		t.Fatalf("round-trip b = %+v ok=%v", pb, ok)
	}
	if _, ok := ParseCandidateIndexKeyCanonical(prefix, "ursm:v2:idx:model:tenant-a:model-a:chat:text"); ok {
		t.Fatal("legacy index key must not parse as k2")
	}
	if _, ok := ParseCandidateIndexKeyCanonical(prefix, "ursm:v2:idx:model:k2:dGVuYW50LWE:bW9kZWwtYQ:Y2hhdA"); ok {
		t.Fatal("missing segment must not parse")
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
	seenWindow := map[string]string{}
	seenIndex := map[string]string{}
	for i := 0; i < 500; i++ {
		tenant, raw := gen(6), gen(9)
		cid := 1 + rng.Intn(100000)
		tupleID := fmt.Sprintf("%q/%d/%q", tenant, cid, raw)

		nodeKey := NodeKeyCanonical(prefix, tenant, cid, raw)
		if prev, dup := seenNode[nodeKey]; dup && prev != tupleID {
			t.Fatalf("node key collision: %s vs %s both encode to %s", prev, tupleID, nodeKey)
		}
		seenNode[nodeKey] = tupleID
		if parsed, schema, ok := ParseNodeKeyCanonical(prefix, nodeKey); !ok || schema != SchemaK2 || parsed.TenantID != tenant || parsed.CredentialID != cid || parsed.RawModel != raw {
			t.Fatalf("iter %d node round-trip = %+v schema=%v ok=%v", i, parsed, schema, ok)
		}

		for _, bucket := range []string{"1m", "5m", "30m"} {
			windowKey := WindowKeyCanonical(prefix, tenant, cid, raw, bucket)
			windowID := tupleID + "/" + bucket
			if prev, dup := seenWindow[windowKey]; dup && prev != windowID {
				t.Fatalf("window key collision: %s vs %s both encode to %s", prev, windowID, windowKey)
			}
			seenWindow[windowKey] = windowID
			if windowKey == nodeKey {
				t.Fatalf("iter %d: window key must not equal node key", i)
			}
			if parsed, ok := ParseWindowKeyCanonical(prefix, windowKey); !ok || parsed.Bucket != bucket || parsed.TenantID != tenant || parsed.CredentialID != cid || parsed.RawModel != raw {
				t.Fatalf("iter %d window round-trip = %+v ok=%v", i, parsed, ok)
			}
		}

		indexKey := CandidateIndexKeyCanonical(prefix, tenant, raw, "chat", "text")
		// The index key legitimately omits the credential ID (the ZSET is
		// per tenant/model/profile/modality), so identity excludes cid.
		indexID := fmt.Sprintf("%q/%q/chat/text", tenant, raw)
		if prev, dup := seenIndex[indexKey]; dup && prev != indexID {
			t.Fatalf("index key collision: %s vs %s both encode to %s", prev, indexID, indexKey)
		}
		seenIndex[indexKey] = indexID
		if parsed, ok := ParseCandidateIndexKeyCanonical(prefix, indexKey); !ok || parsed.TenantID != tenant || parsed.CanonicalModel != raw || parsed.Profile != "chat" || parsed.Modality != "text" {
			t.Fatalf("iter %d index round-trip = %+v ok=%v", i, parsed, ok)
		}
	}
}
