package startup

// migration_961_market_scoring_runs_test.go — 961 契约门（镜像 959 五件套的
// 形态，裁剪到本迁移的实际面：单表台账，无视图/无跨迁移列位）。
//
// 钉什么：
//  ① 形态：列集/类型/主键（精确计数）；自登记行在位（≥952 纪律）；
//  ② down：只 DROP 本表（不触碰 940 三表 / 959 列 / schema_migrations）；
//  ③ 字节一致：canonical ↔ installer embeddata 双副本（六面登记防漏）；
//  ④ tsv 台账行：installed_startup_migrations.tsv 必须含 961 行（第 361 位）；
//  ⑤ 变异臂：去掉列 ⇒ ①红；down 里混入对别表的 DROP ⇒ ②红（守卫有牙）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mig961Name     = "961_market_scoring_runs.sql"
	mig961DownName = "961_market_scoring_runs.down.sql"
)

func mig961Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestMigration961Shape(t *testing.T) {
	root := mig951Tree(t)
	body := mig961Read(t, root, filepath.Join("sql", "migrations", "startup", mig961Name))

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS public.market_scoring_runs",
		"run_date          date PRIMARY KEY",
		"non_trusted_count int NOT NULL DEFAULT 0",
		"detail            jsonb NOT NULL DEFAULT '{}'::jsonb",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("961 shape missing %q", want)
		}
	}
	// 精确列计数：10 列（run_date + 7×int + detail + computed_at）。
	gotCols := strings.Count(body, " int NOT NULL DEFAULT 0") + strings.Count(body, " date PRIMARY KEY") + strings.Count(body, " jsonb NOT NULL DEFAULT '{}'::jsonb") + strings.Count(body, " timestamptz NOT NULL DEFAULT now()")
	if gotCols != 10 {
		t.Fatalf("961 column count drift: want 10 typed columns, counted %d markers", gotCols)
	}
	// 自登记（≥952 纪律；TestMigrationLedgerSelfRegistration 的同族前置）。
	if !strings.Contains(body, "INSERT INTO public.schema_migrations") ||
		!strings.Contains(body, "'961'") {
		t.Fatalf("961 missing its schema_migrations self-registration row")
	}
	// 事务化 + 锁/预算守卫（952+ 现行约定）。
	for _, want := range []string{"BEGIN;", "SET LOCAL lock_timeout", "SET LOCAL statement_timeout", "COMMIT;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("961 missing %q", want)
		}
	}
}

// mig961DownViolations 是 down 门的可测内核：返回违例清单（空 = 通过）。
// 抽出来是为了变异臂能对**同一份判据**注入违例体（守卫自己也要被审计）。
func mig961DownViolations(body string) []string {
	var bad []string
	if !strings.Contains(body, "DROP TABLE IF EXISTS public.market_scoring_runs") {
		bad = append(bad, "must drop its own table market_scoring_runs")
	}
	upper := strings.ToUpper(body)
	for _, banned := range []string{
		"MARKET_PRICE_REFERENCE", "SUPPLIER_MARKET_POSITION",
		"MARKET_PLATFORM_REGISTRY", "SCHEMA_MIGRATIONS",
	} {
		if strings.Contains(upper, "DROP TABLE IF EXISTS PUBLIC."+banned) {
			bad = append(bad, "must not drop "+banned+" (only its own table)")
		}
	}
	return bad
}

func TestMigration961Down(t *testing.T) {
	root := mig951Tree(t)
	body := mig961Read(t, root, filepath.Join("sql", "migrations", "startup", mig961DownName))
	if bad := mig961DownViolations(body); len(bad) > 0 {
		t.Fatalf("961 down violations: %v\n%s", bad, body)
	}
}

func TestMigration961ByteIdenticalEmbeddata(t *testing.T) {
	root := mig951Tree(t)
	for _, name := range []string{mig961Name, mig961DownName} {
		a, err := os.ReadFile(filepath.Join(root, "sql", "migrations", "startup", name))
		if err != nil {
			t.Fatalf("read canonical %s: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if err != nil {
			t.Fatalf("read embeddata %s: %v（六面登记缺口）", name, err)
		}
		if string(a) != string(b) {
			t.Fatalf("%s canonical and embeddata copies differ", name)
		}
	}
}

func TestMigration961TsvAndRegistrations(t *testing.T) {
	root := mig951Tree(t)
	tsv := mig961Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, "361\t961_market_scoring_runs.sql") {
		t.Fatalf("tsv must register 961 at position 361")
	}
	runner := mig961Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"961_market_scoring_runs.sql"`) {
		t.Fatalf("dbinit StartupFiles missing 961")
	}
	apply := mig961Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	for _, want := range []string{
		"961_market_scoring_runs.sql",
		`ledger_artifact_probe "961_market_scoring_runs.sql"`,
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply-db-revision-sequence.sh missing %q", want)
		}
	}
	installer := mig961Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	for _, want := range []string{
		"//go:embed embeddata/startup/961_market_scoring_runs.sql",
		"//go:embed embeddata/startup/961_market_scoring_runs.down.sql",
		`"startup/961_market_scoring_runs.sql":`,
		`"startup/961_market_scoring_runs.down.sql":`,
	} {
		if !strings.Contains(installer, want) {
			t.Fatalf("installer main.go missing %q", want)
		}
	}
}

// TestMigration961MutationArms — 守卫有牙：列被抽掉 / down 混入别表 DROP ⇒ 门红。
func TestMigration961MutationArms(t *testing.T) {
	t.Run("shape gate catches a dropped column", func(t *testing.T) {
		root := mig951Tree(t)
		body := mig961Read(t, root, filepath.Join("sql", "migrations", "startup", mig961Name))
		mutated := strings.Replace(body, "non_trusted_count int NOT NULL DEFAULT 0,", "", 1)
		if mutated == body {
			t.Fatal("mutation anchor missing")
		}
		gotCols := strings.Count(mutated, " int NOT NULL DEFAULT 0") + strings.Count(mutated, " date PRIMARY KEY") + strings.Count(mutated, " jsonb NOT NULL DEFAULT '{}'::jsonb") + strings.Count(mutated, " timestamptz NOT NULL DEFAULT now()")
		if gotCols == 10 {
			t.Fatal("mutant still counts 10 columns — shape gate has no teeth")
		}
	})
	t.Run("down gate catches cross-table drop", func(t *testing.T) {
		root := mig951Tree(t)
		body := mig961Read(t, root, filepath.Join("sql", "migrations", "startup", mig961DownName))
		mutated := body + "\nDROP TABLE IF EXISTS public.market_price_reference;\n"
		if bad := mig961DownViolations(mutated); len(bad) == 0 {
			t.Fatal("down gate accepted a cross-table DROP — gate has no teeth")
		}
	})
}
