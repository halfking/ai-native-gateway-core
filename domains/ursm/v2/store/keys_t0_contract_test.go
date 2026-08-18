package store

import "testing"

func TestTenantScopedKeysSupportedGrammarAreExactAndIsolated(t *testing.T) {
	const prefix = "contract:"
	if got, want := NodeKeyForTenant(prefix, "tenant-a", 7, "model-a"), "contract:node:tenant-a:7:model-a"; got != want {
		t.Fatalf("node key = %q, want %q", got, want)
	}
	if got, want := NodeKeyForTenant(prefix, "7", 7, "model-a"), "contract:node:t:7:7:model-a"; got != want {
		t.Fatalf("numeric tenant node key = %q, want %q", got, want)
	}
	if NodeKeyForTenant(prefix, "tenant-a", 7, "model-a") == NodeKeyForTenant(prefix, "tenant-b", 7, "model-a") {
		t.Fatal("different tenants must not share a node key")
	}
	if got, want := WindowKeyForTenant(prefix, "tenant-a", 7, "model-a", "5m"), "contract:win:5m:tenant-a:7:model-a"; got != want {
		t.Fatalf("window key = %q, want %q", got, want)
	}
	if got, want := CandidateIndexKey(prefix, "tenant-a", "model-a", "chat", "text"), "contract:idx:model:tenant-a:model-a:chat:text"; got != want {
		t.Fatalf("candidate index key = %q, want %q", got, want)
	}
	if CandidateIndexKey(prefix, "tenant-a", "model-a", "chat", "text") == CandidateIndexKey(prefix, "tenant-b", "model-a", "chat", "text") {
		t.Fatal("different tenants must not share a candidate index key")
	}
}
