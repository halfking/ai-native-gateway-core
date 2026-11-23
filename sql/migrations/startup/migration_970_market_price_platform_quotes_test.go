package startup

// migration_970_market_price_platform_quotes_test.go — 970 契约门。
// 钉：形态 / down 只退自己 / embeddata 字节一致 / tsv+六面登记 / 变异臂。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mig970Name     = "970_market_price_platform_quotes.sql"
	mig970DownName = "970_market_price_platform_quotes.down.sql"
)

func mig970Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestMigration970Shape(t *testing.T) {
	root := mig951Tree(t)
	body := mig970Read(t, root, filepath.Join("sql", "migrations", "startup", mig970Name))
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS public.market_price_platform_quotes",
		"PRIMARY KEY (dimension, canonical_name, platform_id)",
		"input_per_1m    numeric(14,6)",
		"cny_out_per_1m  numeric(14,6)",
		"INSERT INTO public.schema_migrations",
		"'970'",
		"BEGIN;", "COMMIT;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("970 shape missing %q", want)
		}
	}
}

func mig970DownViolations(body string) []string {
	var bad []string
	if !strings.Contains(body, "DROP TABLE IF EXISTS public.market_price_platform_quotes") {
		bad = append(bad, "must drop its own table")
	}
	upper := strings.ToUpper(body)
	for _, banned := range []string{
		"MARKET_PRICE_REFERENCE", "SUPPLIER_MARKET_POSITION",
		"MARKET_PLATFORM_REGISTRY", "MARKET_SCORING_RUNS",
	} {
		if strings.Contains(upper, "DROP TABLE IF EXISTS PUBLIC."+banned) {
			bad = append(bad, "must not drop "+banned)
		}
	}
	return bad
}

func TestMigration970Down(t *testing.T) {
	root := mig951Tree(t)
	body := mig970Read(t, root, filepath.Join("sql", "migrations", "startup", mig970DownName))
	if bad := mig970DownViolations(body); len(bad) > 0 {
		t.Fatalf("970 down violations: %v", bad)
	}
}

func TestMigration970ByteIdenticalEmbeddata(t *testing.T) {
	root := mig951Tree(t)
	for _, name := range []string{mig970Name, mig970DownName} {
		a, err := os.ReadFile(filepath.Join(root, "sql", "migrations", "startup", name))
		if err != nil {
			t.Fatalf("canonical %s: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if err != nil {
			t.Fatalf("embeddata %s: %v", name, err)
		}
		if string(a) != string(b) {
			t.Fatalf("%s canonical ≠ embeddata", name)
		}
	}
}

func TestMigration970TsvAndRegistrations(t *testing.T) {
	root := mig951Tree(t)
	tsv := mig970Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, "371\t970_market_price_platform_quotes.sql") {
		t.Fatalf("tsv must register 970 at position 370")
	}
	runner := mig970Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"970_market_price_platform_quotes.sql"`) {
		t.Fatalf("dbinit StartupFiles missing 970")
	}
	apply := mig970Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	for _, want := range []string{
		"970_market_price_platform_quotes.sql",
		`ledger_artifact_probe "970_market_price_platform_quotes.sql"`,
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply script missing %q", want)
		}
	}
	installer := mig970Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	for _, want := range []string{
		"//go:embed embeddata/startup/970_market_price_platform_quotes.sql",
		`"startup/970_market_price_platform_quotes.sql":`,
	} {
		if !strings.Contains(installer, want) {
			t.Fatalf("installer missing %q", want)
		}
	}
}

func TestMigration970MutationArms(t *testing.T) {
	t.Run("shape catches dropped PK", func(t *testing.T) {
		root := mig951Tree(t)
		body := mig970Read(t, root, filepath.Join("sql", "migrations", "startup", mig970Name))
		mutated := strings.Replace(body, "PRIMARY KEY (dimension, canonical_name, platform_id)", "", 1)
		if mutated == body {
			t.Fatal("mutation anchor missing")
		}
		if strings.Contains(mutated, "PRIMARY KEY (dimension, canonical_name, platform_id)") {
			t.Fatal("mutation did not remove PK")
		}
	})
	t.Run("down catches foreign DROP", func(t *testing.T) {
		body := "DROP TABLE IF EXISTS public.market_price_platform_quotes;\nDROP TABLE IF EXISTS public.market_price_reference;\n"
		if bad := mig970DownViolations(body); len(bad) == 0 {
			t.Fatal("expected violation for dropping market_price_reference")
		}
	})
}
