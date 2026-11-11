package startup

// migration_960_ledger_repair_test.go —— 迁移 960 的契约门 + **自登记纪律门**。
//
// 960 = 记账连续性修复（2026-11-12 批判复审发现：950/951/957/958 四个迁移文件
// 缺 schema_migrations 自登记行 ⇒ 211/245/252 记账链三处断层）。判据三件：
//
//	① 形态门：960 自身自登记 + 四个回填各带**对象在场守卫**
//	  （WHERE EXISTS 约束 / to_regclass 索引表）+ ON CONFLICT 幂等。
//	② down 门：删五个记账行、不动对象。
//	③ ★ 自登记纪律门 TestMigrationLedgerSelfRegistration：**≥952 的每个
//	  startup 迁移文件必须自登记**（952–956/959 起的现行约定）——这道门补的
//	  洞 = 「约定活在惯例里、没有机器执行」⇒ 950/951/957/958 四处静默断层
//	  （LESSONS §684 同族：手写惯例必然漂移）。957/958 走豁免表
//	  （字节冻结 applied(211/245/252) + 由 960 守卫回填）。
//	  变异臂：在**临时树**里把 960 的自登记行删掉 ⇒ 同款判据必须点名 960
//	  （门有牙；真树不被触碰）。
//
// 判据纪律：文本级，按标识符精确匹配。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mig960Name     = "960_migration_ledger_continuity_repair.sql"
	mig960DownName = "960_migration_ledger_continuity_repair.down.sql"
)

// selfRegistrationViolations 走一遍「≥952 必须自登记」判据，返回违例文件名。
// dir 参数 = 被审目录（真树或变异临时树）；allowlist = 豁免表。
func selfRegistrationViolations(dir string) ([]string, error) {
	allowlist := map[string]string{
		"957_self_check_error_type_kind_closure_rate_limit_observed.sql": "字节冻结；记账由 960 守卫回填",
		"958_market_platform_registry.sql":                               "字节冻结；记账由 960 守卫回填",
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".down.sql") || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if len(name) < 4 || name[:3] < "952" || name[3] != '_' {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(b), "INSERT INTO public.schema_migrations") {
			continue
		}
		if _, ok := allowlist[name]; ok {
			continue
		}
		violations = append(violations, name)
	}
	return violations, nil
}

func TestMigration960Shape(t *testing.T) {
	root := mig951Tree(t)
	raw := mig951Read(t, root, "sql/migrations/startup/"+mig960Name)
	// 去注释后比对（-- 行注释）——头注里含 "INSERT INTO public.schema_migrations"
	// 字样 ⇒ 不剥注释会把注释算进计数（门自己红）。
	var codeLines []string
	for _, ln := range strings.Split(raw, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		codeLines = append(codeLines, ln)
	}
	body := strings.Join(codeLines, "\n")

	for _, want := range []string{
		// ① 四个守卫式回填
		"WHERE EXISTS (",
		"pg_get_constraintdef(c.oid) LIKE '%credential_recovery%'",
		"pg_get_constraintdef(c.oid) LIKE '%rate_limit_observed%'",
		"to_regclass('public.idx_armor_judgments_created_at') IS NOT NULL",
		"to_regclass('public.market_platform_registry') IS NOT NULL",
		// ② 自登记
		"VALUES ('960'",
		"'950'", "'951'", "'957'", "'958'",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("960 up missing %q", want)
		}
	}
	if got := strings.Count(body, "INSERT INTO public.schema_migrations"); got != 5 {
		t.Fatalf("960 must have exactly 5 schema_migrations INSERTs (4 backfill + 1 self), got %d", got)
	}
	if got := strings.Count(body, "ON CONFLICT (version) DO UPDATE"); got != 5 {
		t.Fatalf("960 must have exactly 5 ON CONFLICT clauses (idempotent replay), got %d", got)
	}
	// 守卫覆盖：4 个回填必须各带 WHERE 守卫（对象在场才记账）
	if got := strings.Count(body, "\nWHERE "); got != 4 {
		t.Fatalf("960 backfills must carry exactly 4 object-presence guards, got %d", got)
	}
}

func TestMigration960Down(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, "sql/migrations/startup/"+mig960DownName)
	for _, want := range []string{
		"DELETE FROM public.schema_migrations WHERE version = '950'",
		"DELETE FROM public.schema_migrations WHERE version = '951'",
		"DELETE FROM public.schema_migrations WHERE version = '957'",
		"DELETE FROM public.schema_migrations WHERE version = '958'",
		"DELETE FROM public.schema_migrations WHERE version = '960'",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("960 down missing %q", want)
		}
	}
	// down 不许动对象（剥对象是 950/951/957/958 各自 down 的职责）
	if strings.Contains(strings.ToUpper(body), "DROP ") {
		t.Fatalf("960 down must not DROP objects (ledger-only repair)")
	}
}

// TestMigrationLedgerSelfRegistration —— 真树：违例必须为空（957/958 走豁免）。
func TestMigrationLedgerSelfRegistration(t *testing.T) {
	root := mig951Tree(t)
	violations, err := selfRegistrationViolations(filepath.Join(root, "sql", "migrations", "startup"))
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("startup migrations lacking self-registration (记账链断层, 950/951/957/958 同款): %v", violations)
	}
}

// TestMigrationLedgerSelfRegistrationMutation —— 变异臂：在**临时树**里把 960 的
// 自登记行删掉 ⇒ 同款判据必须点名 960（门有牙；真树不被触碰）。
func TestMigrationLedgerSelfRegistrationMutation(t *testing.T) {
	root := mig951Tree(t)
	srcDir := filepath.Join(root, "sql", "migrations", "startup")

	tmp := t.TempDir()
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		content := string(b)
		if e.Name() == mig960Name {
			// ★ 变异形态 = 「按 957/958 的旧写法写 960」：INSERT 目标整表名改写
			//   （不得用 schema_migrations 的前缀名——Contains 判据会误匹配）
			//   ⇒ 文件不含自登记 marker ⇒ 判据必须点名 960。
			content = strings.ReplaceAll(content,
				"INSERT INTO public.schema_migrations", "INSERT INTO public.ledger_backfill_target")
			mutated = true
		}
		if err := os.WriteFile(filepath.Join(tmp, e.Name()), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !mutated {
		t.Fatalf("mutation anchor missing: 960 not found in tree")
	}
	violations, err := selfRegistrationViolations(tmp)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range violations {
		if v == mig960Name {
			found = true
		}
	}
	if !found {
		t.Fatalf("mutation arm: 960 without self-registration was NOT flagged (门无牙); violations=%v", violations)
	}
}
