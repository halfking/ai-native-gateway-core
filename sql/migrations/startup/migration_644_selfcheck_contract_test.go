package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration644SelfCheckTaxonomyMatchesEmbeddedCopy(t *testing.T) {
	source, err := os.ReadFile("644_tuning_views_selfcheck_and_candidate_failure_cache.sql")
	if err != nil {
		t.Fatal(err)
	}

	embeddedPath := filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", "644_tuning_views_selfcheck_and_candidate_failure_cache.sql")
	embedded, err := os.ReadFile(embeddedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != string(embedded) {
		t.Fatal("startup migration 644 differs from installer embedded copy")
	}

	for _, want := range []string{
		"self_check_runs_error_type_check",
		"'quota_periodic'",
		"'model_deprecated'",
		"'upstream_fail'",
		") NOT VALID;",
		"self_check_runs_selection_strategy_check",
		"'no_eligible_model'",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("migration 644 missing canonical self-check taxonomy fragment %q", want)
		}
	}
}

func TestMigration644ErrorTypeGuardIsDefinitionAware(t *testing.T) {
	body, err := os.ReadFile("644_tuning_views_selfcheck_and_candidate_failure_cache.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"position('quota_periodic' in pg_get_constraintdef(oid)) > 0",
		"position('model_deprecated' in pg_get_constraintdef(oid)) > 0",
		"DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("migration 644 error taxonomy guard missing %q", want)
		}
	}
}
