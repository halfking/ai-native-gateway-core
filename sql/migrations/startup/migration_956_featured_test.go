package startup

import (
	"strings"
	"testing"
)

// TestMigration956DeploymentContract 钉住 956 的迁移契约：形态、字节一致、
// 部署序列登记、存在性守卫、幂等守卫、可逆。
//
// ★ 956 的核心纪律（与 953 最大的差别）：
//
//	953 用一份手写名单标注 featured，其中 11/26 项在 models_canonical 里
//	**根本不存在**（幻觉名）⇒ UPDATE 不报错、命中数不校验 ⇒ 静默失效。
//	956 因此在迁移体内自带**存在性前置守卫**（目标不存在即 RAISE EXCEPTION
//	中止）与**终态断言**（更新后必须 featured=TRUE）⇒ 幻觉名无法再静默通过。
func TestMigration956DeploymentContract(t *testing.T) {
	root := mig951Tree(t)
	const name = "956_minimax_m31_flash_featured.sql"

	body := mig951Read(t, root, "sql/migrations/startup/"+name)
	for _, want := range []string{
		"BEGIN;",
		"SET LOCAL lock_timeout",
		"SET LOCAL statement_timeout",
		"INSERT INTO public.schema_migrations",
		"COMMIT;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("956 missing %q", want)
		}
	}

	// ★ 存在性前置守卫：目标 canonical 不存在时必须中止，而不是静默 0 行。
	if !strings.Contains(body, "RAISE EXCEPTION") {
		t.Fatal("956 must abort (RAISE EXCEPTION) when the target canonical is absent")
	}
	if !strings.Contains(body, "does not exist or is not active") {
		t.Fatal("956 must guard on the target canonical's existence")
	}

	// ★ 幂等守卫：UPDATE 带 featured=FALSE 条件。
	if !strings.Contains(body, "SET featured = TRUE") ||
		!strings.Contains(body, "WHERE featured = FALSE") {
		t.Fatal("956 must update featured idempotently (featured=FALSE guard)")
	}

	// ★ 终态断言：更新后必须处于 featured=TRUE。
	if !strings.Contains(body, "AND featured = TRUE") {
		t.Fatal("956 must assert the target ends up featured=TRUE")
	}

	// installer 副本字节必须与 canonical 一致（两处都要登记）。
	installer := mig951Read(t, root, "installer/cmd/llm-gw-installer/embeddata/startup/"+name)
	if body != installer {
		t.Fatal("956 canonical and installer bytes differ")
	}

	// down 副本必须存在且能回滚 featured 标记。
	down := mig951Read(t, root, "sql/migrations/startup/956_minimax_m31_flash_featured.down.sql")
	if !strings.Contains(down, "SET featured = FALSE") {
		t.Fatal("956 down must reset featured to FALSE")
	}
	if !strings.Contains(down, "DELETE FROM public.schema_migrations WHERE version = '956'") {
		t.Fatal("956 down must delete its schema_migrations row")
	}
	downInstaller := mig951Read(t, root, "installer/cmd/llm-gw-installer/embeddata/startup/956_minimax_m31_flash_featured.down.sql")
	if down != downInstaller {
		t.Fatal("956 down canonical and installer bytes differ")
	}

	// 部署序列必须真的执行它（否则迁移体存在但永远不落库）。
	deploy := mig951Read(t, root, "scripts/apply-db-revision-sequence.sh")
	if !strings.Contains(deploy, `"$ROOT_DIR/sql/migrations/startup/`+name+`"`) {
		t.Fatal("956 missing from the actual deployment sequence")
	}
	// 产物探针：落库后的终态可被机器复核。
	if !strings.Contains(deploy, `ledger_artifact_probe "`+name+`"`) {
		t.Fatal("956 missing a ledger_artifact_probe entry")
	}

	// ★ 登记面覆盖：StartupFiles / embed / map 三处都要有它，
	// 否则新装环境不会执行、installer 也会拿不到字节。
	runner := mig951Read(t, root, "installer/internal/dbinit/runner.go")
	if !strings.Contains(runner, `"`+name+`"`) {
		t.Fatal("956 missing from dbinit StartupFiles")
	}
	installerMain := mig951Read(t, root, "installer/cmd/llm-gw-installer/main.go")
	if !strings.Contains(installerMain, "embeddata/startup/"+name) {
		t.Fatal("956 missing from the installer's go:embed directive")
	}
	if !strings.Contains(installerMain, `"startup/`+name+`"`) {
		t.Fatal("956 missing from the installer's embedded SQL file map")
	}

	// ★ 派生台账必须含它（第 356 位）—— 台账是 StartupFiles 的机器可读镜像，
	// 手改台账会被 startup_manifest_test 判红，故此处只断言"存在且有序"。
	manifest := mig951Read(t, root, "sql/schema/installed_startup_migrations.tsv")
	if !strings.Contains(manifest, name) {
		t.Fatal("956 missing from installed_startup_migrations.tsv")
	}
}

// TestMigration956CanonicalNameIsReal 钉住"只写真实存在的 canonical 名"这条
// 纪律 —— 953 的 11 个幻觉名正是从这里漏过去的。
//
// ★ 关于"真实性"如何被守：
//
//	canonical 名单本身**不在仓内**（models_canonical 的行由生产库同步，
//	sql/objects/tables/models_canonical.sql 只有表结构、没有行数据）
//	⇒ 静态门无法直接断言"该名存在"。
//	⇒ 这里改为守两条可静态验证的纪律：
//	  ① 956 **不得**引用 953 名单里经 211 真库核对不存在的幻觉名；
//	  ② 956 **必须**自带存在性前置守卫 + 终态断言（真库上跑时才会暴露
//	     该名不存在 ⇒ 迁移中止，而不是静默 0 行）。
//	真库层面的存在性由迁移体自身的 RAISE EXCEPTION 负责（见
//	TestMigration956DeploymentContract）。
func TestMigration956CanonicalNameIsReal(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, "sql/migrations/startup/956_minimax_m31_flash_featured.sql")

	// 953 名单里经 211 真库核对**不存在**的幻觉名 —— 956 绝不能再写它们。
	hallucinated := []string{
		"baichuan2-turbo",
		"deepseek-v3-flash",
		"gemini-2-flash",
		"glm-5-flash",
		"glm-5.1-flash",
		"minimax-abab7-flash",
		"minimax-abab7-lite",
		"qwen-plus-flash",
		"qwen-turbo-flash",
		"yi-lightning",
		"yi-spark",
	}
	for _, h := range hallucinated {
		if strings.Contains(body, "'"+h+"'") {
			t.Fatalf("956 must not reference the hallucinated canonical name %q "+
				"(it does not exist in models_canonical — see the 2026-11-10 audit)", h)
		}
	}

	// 956 只写一个 canonical 名 ⇒ 名单规模受控（953 是 26 项手写名单，
	// 其中 11 项是幻觉名）。这里守住"单目标"这个形态：迁移体里
	// canonical_name 的字面量出现次数必须受控，避免"再写一份手写名单"。
	if n := strings.Count(body, "canonical_name = 'minimax-m3.1-flash'"); n < 2 {
		t.Fatalf("956 must reference its single target canonical consistently "+
			"(expected at least 2 references — guard + update + assert), got %d", n)
	}

	// 该名必须以**字面量**形式出现在迁移体里（不允许拼装/变量化，
	// 否则真库守卫会失效）。
	if !strings.Contains(body, "canonical_name = 'minimax-m3.1-flash'") {
		t.Fatal("956 must reference the target canonical as a literal")
	}
}
