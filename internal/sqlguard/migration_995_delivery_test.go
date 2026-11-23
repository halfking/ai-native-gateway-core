package sqlguard

// 995（错误处理手册轮）投递门：error_playbook 两表 + 种子条目必须在
// 每一条交付通道上齐备。
//
// ★ 为什么需要这道门：手册的管理面/worker 在「表未建出」的部署上必须
//
//	可区分（503 + 明确提示，而不是 500 / 静默空转）——与 980 同款纪律，
//	把「交付面断裂」变成 CI 可判的红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pb995File = "995_error_playbook.sql"

func TestMigration995ErrorPlaybookDelivered(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", pb995File)
	body, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 995 正本失败（投递面断裂）: %v", err)
	}
	// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
	exec := sgStripSQLComments(string(body))
	rawUp := string(body)

	// ① 两张表必须幂等建出（可被内容指纹重放）。
	for _, ddl := range []string{
		"CREATE TABLE IF NOT EXISTS public.error_playbook_entries",
		"CREATE TABLE IF NOT EXISTS public.error_playbook_executions",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_error_playbook_type_pattern",
	} {
		if !strings.Contains(exec, ddl) {
			t.Fatalf("995 缺幂等 DDL: %s", ddl)
		}
	}
	// ② 方案形态闭集 + 执行状态闭集必须与 Go 侧常量同口径。
	for _, check := range []string{
		"plan_kind IN ('script', 'api', 'prompt')",
		"source IN ('seed', 'manual', 'pi_analysis', 'refinement')",
		"trigger_source IN ('standard', 'auto_analyze', 'manual', 'refinement')",
		"status IN ('succeeded', 'failed', 'skipped', 'awaiting_approval', 'no_action')",
	} {
		if !strings.Contains(exec, check) {
			t.Fatalf("995 缺闭集约束: %s（Go 侧常量与 DB 口径漂移）", check)
		}
	}
	// ③ 种子条目：5 条 seed、全部 auto_execute=false（fail-closed 默认）。
	if !strings.Contains(exec, `ON CONFLICT (error_type, COALESCE(match_pattern, '')) DO NOTHING`) {
		t.Fatalf("995 种子缺表达式唯一键 ON CONFLICT ⇒ 重放不幂等")
	}
	if n := strings.Count(exec, "'seed', TRUE, FALSE, 'seed:995'"); n != 5 {
		t.Fatalf("995 种子条目数=%d（want 5，且必须 source='seed' enabled=TRUE auto_execute=FALSE）", n)
	}
	// ④ 台账自登记。
	if !strings.Contains(exec, "VALUES ('995',") {
		t.Fatalf("995 缺 schema_migrations 自登记（≥952 纪律）")
	}

	// ⑤ embeddata 副本逐字一致（installer 强制门之外的双保险）。
	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", pb995File)
	embedBody, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("读 995 embeddata 副本失败: %v", err)
	}
	if string(embedBody) != rawUp {
		t.Fatalf("995 正本与 embeddata 副本不一致（installer 会应用另一份内容）")
	}
	down := filepath.Join("..", "..", "sql", "migrations", "startup", "995_error_playbook.down.sql")
	if _, err := os.Stat(down); err != nil {
		t.Fatalf("995 缺 down 迁移: %v", err)
	}
	embedDown := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", "995_error_playbook.down.sql")
	embedDownBody, err := os.ReadFile(embedDown)
	if err != nil {
		t.Fatalf("读 995 down embeddata 副本失败: %v", err)
	}
	downBody, _ := os.ReadFile(down)
	if string(embedDownBody) != string(downBody) {
		t.Fatalf("995 down 正本与 embeddata 副本不一致")
	}

	// ⑥ docker 部署通道的序列清单 + 产物探针（211/154 的第六/七处登记面）。
	seq, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	// ★ 精确到 files=(...) 的整条条目（★ 只判「文件名出现过」⇒ 同名出现在
	//   产物探针行里也会命中 ⇒ ★ 注释掉 files 条目照样绿 ⇒ ★ 实测本轮踩到）。
	if !strings.Contains(sgStripShellComments(string(seq)),
		`"$ROOT_DIR/sql/migrations/startup/`+pb995File+`"`) {
		t.Fatalf("docker 部署通道序列清单缺 995 ⇒ 211 部署不会应用该迁移")
	}
	if !strings.Contains(sgStripShellComments(string(seq)), `ledger_artifact_probe "995_error_playbook.sql"`) {
		t.Fatalf("docker 部署通道缺 995 产物探针（指纹命中跳过路径无法对账）")
	}

	// ⑦ 安装器 runner 链（新装环境的投递面）。
	runner, err := os.ReadFile(filepath.Join("..", "..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("读 dbinit runner 失败: %v", err)
	}
	if !strings.Contains(string(runner), `"995_error_playbook.sql",`) {
		t.Fatalf("dbinit runner StartupFiles 缺 995 ⇒ 新装环境建不出手册表")
	}

	// ⑧ installer main.go 的 embed + 映射（embeddata 副本成死文件的防线）。
	instMain, err := os.ReadFile(filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "main.go"))
	if err != nil {
		t.Fatalf("读 installer main.go 失败: %v", err)
	}
	inst := string(instMain)
	for _, marker := range []string{
		"//go:embed embeddata/startup/995_error_playbook.sql",
		"//go:embed embeddata/startup/995_error_playbook.down.sql",
		`"startup/995_error_playbook.sql":      errorPlaybook995`,
		`"startup/995_error_playbook.down.sql": errorPlaybook995Down`,
	} {
		if !strings.Contains(inst, marker) {
			t.Fatalf("installer main.go 缺 995 登记: %s", marker)
		}
	}

	// ⑨ 台账行（installed_startup_migrations.tsv）。
	manifest, err := os.ReadFile(filepath.Join("..", "..", "sql", "schema", "installed_startup_migrations.tsv"))
	if err != nil {
		t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
	}
	if !strings.Contains(string(manifest), "995_error_playbook.sql") {
		t.Fatalf("installed_startup_migrations.tsv 缺 995 行")
	}

	// ⑩ 离线升级通道副本（installer/migrations/，offline_apply.go 消费）。
	// 2026-11-21 审计轮补齐：993/994 均有此面，995 缺 ⇒ 离线升级包不会应用
	// 该迁移（子审计 E 发现，与 994 的 migration_994_delivery_test 同款钉法）。
	offlineUp, err := os.ReadFile(filepath.Join("..", "..", "installer", "migrations",
		"995_error_playbook", "up.sql"))
	if err != nil {
		t.Fatalf("读 995 installer/migrations 副本失败: %v", err)
	}
	if string(offlineUp) != rawUp {
		t.Fatalf("995 正本与 installer/migrations 副本不一致")
	}
	offlineDown, err := os.ReadFile(filepath.Join("..", "..", "installer", "migrations",
		"995_error_playbook", "down.sql"))
	if err != nil {
		t.Fatalf("读 995 installer/migrations down 副本失败: %v", err)
	}
	if string(offlineDown) != string(downBody) {
		t.Fatalf("995 down 正本与 installer/migrations 副本不一致")
	}
}
