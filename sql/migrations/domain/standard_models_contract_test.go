package domain_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(data)
}

func TestStandardModelSeedsStayIdentical(t *testing.T) {
	paths := [][]string{
		{"sql", "schema", "02-seed.sql"},
		{"deploy", "sql", "schemas", "baseline", "02-seed.sql"},
		{"installer", "cmd", "llm-gw-installer", "embeddata", "02-seed.sql"},
	}
	want := readRepoFile(t, paths[0]...)
	for _, path := range paths[1:] {
		if got := readRepoFile(t, path...); got != want {
			t.Fatalf("seed drift: %v differs from %v", path, paths[0])
		}
	}
}

func TestBindingPlaceholderMigrationUsesProviderCodesAndProvenance(t *testing.T) {
	sql := readRepoFile(t, "sql", "migrations", "domain", "362_standard_binding_placeholders.sql")
	for _, code := range []string{"'xai'", "'moonshot'", "'google-gemini'"} {
		if !strings.Contains(sql, "p.code = "+code) {
			t.Errorf("migration does not resolve provider by code %s", code)
		}
	}
	for _, id := range []string{"provider_id = 30", "provider_id = 17", "provider_id = 10"} {
		if strings.Contains(sql, id) {
			t.Errorf("migration still contains environment-specific provider id %s", id)
		}
	}
	if !strings.Contains(sql, `"source":"migration-362"`) {
		t.Fatal("placeholder migration lacks provenance marker")
	}

	down := readRepoFile(t, "sql", "migrations", "domain", "362_standard_binding_placeholders.down.sql")
	if !strings.Contains(down, "plan_meta->>'source' = 'migration-362'") {
		t.Fatal("down migration does not constrain deletion to migration-owned placeholders")
	}
}
