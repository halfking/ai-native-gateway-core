package bootstrap

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Schema-aware bootstrap targets (doc 14 §5.1-§5.3): legacy mode writes the
// frozen legacy bytes, dual writes both grammars, canonical writes canonical
// only and refuses a tuple canonical cannot represent.

func TestMapRowNodesSchemaModes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	row := probeRow{TenantID: "tenant-a", CredentialID: 7, RawModel: "m:x"}

	legacy, err := mapRowNodes(row, "p:", 300, now, store.KeySchemaModeLegacy)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if len(legacy) != 1 || legacy[0].Key != "p:node:tenant-a:7:m:x" {
		t.Fatalf("legacy nodes = %+v", legacy)
	}

	dual, err := mapRowNodes(row, "p:", 300, now, store.KeySchemaModeDual)
	if err != nil {
		t.Fatalf("dual: %v", err)
	}
	if len(dual) != 2 {
		t.Fatalf("dual node count=%d, want 2 (both grammars)", len(dual))
	}
	if dual[0].Key != "p:node:tenant-a:7:m:x" {
		t.Fatalf("dual legacy key = %q", dual[0].Key)
	}
	k2Want, err := store.K2NodeKeyForTenant("p:", "tenant-a", 7, "m:x")
	if err != nil || dual[1].Key != k2Want {
		t.Fatalf("dual canonical key = %q, want %q (err=%v)", dual[1].Key, k2Want, err)
	}

	canon, err := mapRowNodes(row, "p:", 300, now, store.KeySchemaModeCanonical)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if len(canon) != 1 || canon[0].Key != k2Want {
		t.Fatalf("canonical nodes = %+v, want single canonical key %q", canon, k2Want)
	}
}

func TestMapRowNodesEmptyTenant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	row := probeRow{TenantID: "", CredentialID: 7, RawModel: "m"}

	if _, err := mapRowNodes(row, "p:", 300, now, store.KeySchemaModeCanonical); err == nil {
		t.Fatal("canonical bootstrap must refuse an empty-tenant row: canonical cannot represent it")
	}
	dual, err := mapRowNodes(row, "p:", 300, now, store.KeySchemaModeDual)
	if err != nil {
		t.Fatalf("dual: %v", err)
	}
	if len(dual) != 1 || dual[0].Key != "p:node:7:m" {
		t.Fatalf("dual empty tenant = %+v, want legacy-only write", dual)
	}
}
