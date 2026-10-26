package startup_test

import (
	"os"
	"strings"
	"testing"
)

func TestMigration868DefinesBindingScopedStreamMode(t *testing.T) {
	body, err := os.ReadFile("868_credential_model_stream_mode.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(body))
	for _, want := range []string{
		"add column if not exists stream_mode text not null default 'both'",
		"check (stream_mode in ('both', 'stream_only', 'non_stream_only'))",
		"lower(p.code) = 'vapeur'",
		"lower(pm.raw_model_name) like 'claude%'",
		"cmb.stream_mode is distinct from 'non_stream_only'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	if strings.Contains(sql, "where lower(p.code) = 'vapeur'\n  and cmb.stream_mode") {
		t.Fatal("Vapeur override is not scoped to Claude model names")
	}
}

func TestMigration868RollbackRemovesOnlyStreamMode(t *testing.T) {
	body, err := os.ReadFile("868_credential_model_stream_mode.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(body))
	if !strings.Contains(sql, "drop column if exists stream_mode") {
		t.Fatal("rollback must remove stream_mode")
	}
	for _, forbidden := range []string{"drop table", "delete from", "truncate"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("rollback contains destructive operation %q", forbidden)
		}
	}
}
