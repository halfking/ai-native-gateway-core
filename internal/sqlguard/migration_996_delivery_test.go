package sqlguard

// 996（W1-Q1 租户预算硬拦截开关轮）投递门：tenant_budgets.enforcement_mode
// 必须在每一条交付通道上齐备。
//
// ★ 为什么需要这道门：W1-Q1 的语义是「warn 默认零变化、hard 才拦 429」——
// 若交付面断裂（漏 embeddata 副本 / 漏 tsv / 漏 docker 通道），新装环境的
// tenant_budgets 没有该列，Go 侧查询直接 42703 ⇒ 预算行读不到 ⇒ 门惰性，
// hard 拦截静默失效且无从报警 —— 与 980/995 同款纪律，把「交付面断裂」
// 变成 CI 可判的红。
//
// ★ 同时钉死「为什么是新迁移而不是改 938 字节」：938 已 applied+verified
// （deploy 245 build_seq 2929），改字节走 content replay 通道连锁 break
// （914/928 教训）——本文件若发现 938 被改动（SHA 与台账不符）也拦不住
// （那归 verify-migration-checksums），但本门把 1000 自身的闭集与幂等形态
// 钉死，防止后人「顺手改 938」时把闭集语义带走。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tbem1000File = "996_tenant_budget_enforcement_mode.sql"

func TestMigration996TenantBudgetEnforcementDelivered(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", tbem1000File)
	body, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 1000 正本失败（投递面断裂）: %v", err)
	}
	exec := string(body)

	// ① 幂等加列（「表已在但列不齐」的环境修得回来 —— 932 补列教训）。
	for _, ddl := range []string{
		"ADD COLUMN IF NOT EXISTS enforcement_mode varchar NOT NULL DEFAULT 'warn'",
	} {
		if !strings.Contains(exec, ddl) {
			t.Fatalf("1000 缺幂等 DDL: %s", ddl)
		}
	}
	// ② 闭集形态必须与 Go 侧常量同口径（warn/hard；DB 侧单点真源）。
	if !strings.Contains(exec, "CHECK (enforcement_mode IN ('warn', 'hard'))") {
		t.Fatalf("1000 缺闭集 CHECK（Go 侧 maas.BudgetEnforcement* 常量与 DB 口径漂移）")
	}
	if !strings.Contains(exec, "tenant_budgets_enforcement_mode_check") {
		t.Fatalf("1000 缺具名约束 tenant_budgets_enforcement_mode_check（探针与幂等 DO 块都按名对拍）")
	}
	// ③ 幂等 DO 块（PostgreSQL 无 ADD CONSTRAINT IF NOT EXISTS ⇒ 必须显式守卫）。
	for _, frag := range []string{
		"IF NOT EXISTS (",
		"SELECT 1 FROM pg_constraint",
		"END IF;",
	} {
		if !strings.Contains(exec, frag) {
			t.Fatalf("1000 的 CHECK 约束缺幂等守卫片段: %s", frag)
		}
	}
	// ④ 超时守卫（995 同口径：会话级 SET，docker 流式通道 SET LOCAL 静默失效）。
	for _, guard := range []string{
		"SET lock_timeout = '5s';",
		"SET statement_timeout = '10min';",
	} {
		if !strings.Contains(exec, guard) {
			t.Fatalf("1000 缺超时守卫: %s", guard)
		}
	}
	// ⑤ 台账自登记（≥952 纪律）。
	if !strings.Contains(exec, "VALUES ('996',") {
		t.Fatalf("1000 缺 schema_migrations 自登记")
	}
	// ⑥ down 只退自己引入的列（迁移链每一步只回退自己引入的那部分 —— 930 处置）。
	down := filepath.Join("..", "..", "sql", "migrations", "startup", "996_tenant_budget_enforcement_mode.down.sql")
	downBody, err := os.ReadFile(down)
	if err != nil {
		t.Fatalf("1000 缺 down 迁移: %v", err)
	}
	downExec := string(downBody)
	for _, frag := range []string{
		"DROP COLUMN IF EXISTS enforcement_mode",
		"DELETE FROM public.schema_migrations WHERE version = '996'",
	} {
		if !strings.Contains(downExec, frag) {
			t.Fatalf("1000 down 缺片段: %s", frag)
		}
	}
	if strings.Contains(downExec, "DROP TABLE") {
		t.Fatalf("1000 down 不得 DROP TABLE（表是 938 的产物，down 只退本迁移引入的列）")
	}

	// ⑦ embeddata 副本逐字一致（installer 会应用另一份内容 = 投递分裂）。
	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", tbem1000File)
	embedBody, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("读 1000 embeddata 副本失败: %v", err)
	}
	if string(embedBody) != exec {
		t.Fatalf("1000 正本与 embeddata 副本不一致（installer 会应用另一份内容）")
	}
	embedDown := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", "996_tenant_budget_enforcement_mode.down.sql")
	embedDownBody, err := os.ReadFile(embedDown)
	if err != nil {
		t.Fatalf("读 1000 down embeddata 副本失败: %v", err)
	}
	if string(embedDownBody) != downExec {
		t.Fatalf("1000 down 正本与 embeddata 副本不一致")
	}

	// ⑧ installer go:embed + embeddedSQLFiles map 双登记。
	mainGo := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "main.go")
	mainSrc, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("读 installer main.go 失败: %v", err)
	}
	main := string(mainSrc)
	for _, frag := range []string{
		"//go:embed embeddata/startup/" + tbem1000File,
		"//go:embed embeddata/startup/996_tenant_budget_enforcement_mode.down.sql",
		`"startup/996_tenant_budget_enforcement_mode.sql":      tenantBudgetEnforcementMode996`,
		`"startup/996_tenant_budget_enforcement_mode.down.sql": tenantBudgetEnforcementMode996Down`,
	} {
		if !strings.Contains(main, frag) {
			t.Fatalf("installer main.go 缺 1000 登记: %s", frag)
		}
	}

	// ⑨ dbinit StartupFiles（新装库跑 1000）。
	runnerGo := filepath.Join("..", "..", "installer", "internal", "dbinit", "runner.go")
	runnerSrc, err := os.ReadFile(runnerGo)
	if err != nil {
		t.Fatalf("读 dbinit runner.go 失败: %v", err)
	}
	if !strings.Contains(string(runnerSrc), `"996_tenant_budget_enforcement_mode.sql",`) {
		t.Fatalf("dbinit runner.go StartupFiles 缺 1000 ⇒ 全新安装的 tenant_budgets 无 enforcement_mode 列")
	}

	// ⑩ tsv 台账（期望索引；996 排在 995 之后 = index 387。该文件由
	// startup_manifest_test 从 StartupFiles 派生，禁止手改）。
	tsv := filepath.Join("..", "..", "sql", "schema", "installed_startup_migrations.tsv")
	tsvBody, err := os.ReadFile(tsv)
	if err != nil {
		t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
	}
	if !strings.Contains(string(tsvBody), "387\t996_tenant_budget_enforcement_mode.sql") {
		t.Fatalf("tsv 缺 996 登记行（387\\t996_tenant_budget_enforcement_mode.sql）")
	}

	// ⑪ docker 通道：files 列表 + 产物探针。
	deploy := filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh")
	deploySrc, err := os.ReadFile(deploy)
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	deployText := string(deploySrc)
	for _, frag := range []string{
		`"$ROOT_DIR/sql/migrations/startup/996_tenant_budget_enforcement_mode.sql"`,
		`ledger_artifact_probe "996_tenant_budget_enforcement_mode.sql"`,
	} {
		if !strings.Contains(deployText, frag) {
			t.Fatalf("apply-db-revision-sequence.sh 缺 1000 登记: %s（docker 通道会静默跳过该迁移）", frag)
		}
	}

	// ⑫ 离线升级通道副本（installer/migrations/，offline_apply.go 消费）——
	// 与 993/994/995 同族（995 曾缺此面，2026-11-21 审计轮补齐）。
	offlineUp, err := os.ReadFile(filepath.Join("..", "..", "installer", "migrations",
		"996_tenant_budget_enforcement_mode", "up.sql"))
	if err != nil {
		t.Fatalf("读 1000 installer/migrations 副本失败: %v", err)
	}
	if string(offlineUp) != exec {
		t.Fatalf("1000 正本与 installer/migrations 副本不一致")
	}
	offlineDown, err := os.ReadFile(filepath.Join("..", "..", "installer", "migrations",
		"996_tenant_budget_enforcement_mode", "down.sql"))
	if err != nil {
		t.Fatalf("读 1000 installer/migrations down 副本失败: %v", err)
	}
	if string(offlineDown) != downExec {
		t.Fatalf("1000 down 正本与 installer/migrations 副本不一致")
	}
}
