package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration621AddsResolvedProviderErrorCleanupIndex(t *testing.T) {
	up, err := os.ReadFile("621_provider_error_details_cleanup_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(up))
	for _, want := range []string{
		"create index if not exists idx_ped_resolved_updated_at",
		"on public.provider_error_details (updated_at)",
		"where resolved = true",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("migration 621 missing %q", want)
		}
	}

	down, err := os.ReadFile("621_provider_error_details_cleanup_index.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(down)), "drop index if exists public.idx_ped_resolved_updated_at") {
		t.Fatal("migration 621 down script does not drop cleanup index")
	}
}
