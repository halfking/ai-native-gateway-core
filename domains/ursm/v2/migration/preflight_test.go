package migration

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Pure classification per doc 14 §4 and doc 15 §2: a legacy key is
// migratable only when the parser uniquely recovers the tuple AND
// re-encoding the tuple with NodeKeyForTenant reproduces the exact source
// bytes (the machine decision for the a:7:b:8:c collision family).

func TestClassifyMigratable(t *testing.T) {
	for _, key := range []string{
		"p:node:tenant-a:7:model-a",
		"p:node:t:123:7:model-a", // numeric tenant, tagged form
		"p:node:a:7:b",           // untagged string tenant, exact segment count
	} {
		c := ClassifyKey("p:", key)
		if c.Class != ClassMigratable {
			t.Fatalf("%q = %s (%s), want migratable", key, c.Class, c.Reason)
		}
		if c.Tuple == nil {
			t.Fatalf("%q: migratable must carry the recovered tuple", key)
		}
	}
}

func TestClassifyAmbiguous(t *testing.T) {
	// Round-trip failure: both parses succeed textually but re-encoding
	// yields a different key, so the tuple cannot be proven unique.
	for _, key := range []string{
		"p:node:a:7:b:8:c",      // frozen collision family (doc 14 §1)
		"p:node:7:model-a",      // empty tenant: uniquely parsed but has no canonical target
		"p:node:k2:7:model",     // reserved k2 marker in legacy position
		"p:node:tenant:x:model", // non-decimal credential
		"p:win:1m:a:7:b:8:c",    // ambiguous window key
	} {
		c := ClassifyKey("p:", key)
		if c.Class != ClassAmbiguous {
			t.Fatalf("%q = %s (%s), want ambiguous", key, c.Class, c.Reason)
		}
		if c.Tuple != nil {
			t.Fatalf("%q: ambiguous must not carry a guessed tuple", key)
		}
	}
}

func TestClassifyExcludedNonTargetNamespaces(t *testing.T) {
	for _, key := range []string{
		"p:node:tenant-a:7:model-a:request_dedup:ab12", // derived dedup marker
		"p:binding:7:model",
		"p:credential:7",
		"p:provider:3",
		"p:meta:ready",
	} {
		c := ClassifyKey("p:", key)
		if c.Class != ClassExcludedNonAuthoritative {
			t.Fatalf("%q = %s (%s), want excluded_non_authoritative", key, c.Class, c.Reason)
		}
	}
}

func TestClassifyCanonicalSourceKey(t *testing.T) {
	k2Key, err := store.K2NodeKeyForTenant("p:", "tenant-a", 7, "model-a")
	if err != nil {
		t.Fatalf("k2 key: %v", err)
	}
	c := ClassifyKey("p:", k2Key)
	if c.Class != ClassCanonicalPresent {
		t.Fatalf("k2 source = %s (%s), want canonical_present", c.Class, c.Reason)
	}
	if c.Schema != store.KeySchemaK2 || c.Tuple == nil || c.Tuple.TenantID != "tenant-a" {
		t.Fatalf("k2 classification = %+v", c)
	}
}
