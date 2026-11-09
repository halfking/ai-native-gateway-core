package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration953DeploymentContract(t *testing.T) {
	root := mig951Tree(t)
	const name = "953_models_canonical_featured.sql"
	body := mig951Read(t, root, "sql/migrations/startup/"+name)
	for _, want := range []string{"BEGIN;", "SET LOCAL lock_timeout", "SET LOCAL statement_timeout", "INSERT INTO public.schema_migrations", "COMMIT;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("953 missing %q", want)
		}
	}
	installer := mig951Read(t, root, "installer/cmd/llm-gw-installer/embeddata/startup/"+name)
	if body != installer {
		t.Fatal("953 canonical and installer bytes differ")
	}
	deploy := mig951Read(t, root, "scripts/apply-db-revision-sequence.sh")
	if !strings.Contains(deploy, `"$ROOT_DIR/sql/migrations/startup/`+name+`"`) {
		t.Fatal("953 missing from the actual deployment sequence")
	}
	// COMMENT IS accepts a string literal, not concatenation expressions.
	comment := strings.Split(strings.Split(body, "COMMENT ON COLUMN")[1], ";")[0]
	if strings.Contains(comment, "||") {
		t.Fatal("953 COMMENT uses an expression that PostgreSQL cannot parse")
	}
	objects, err := os.ReadFile(root + "/sql/objects/tables/models_canonical.sql")
	if err != nil || !strings.Contains(string(objects), "featured boolean DEFAULT false NOT NULL") {
		t.Fatal("new installs must also have models_canonical.featured")
	}
}
