package sqlguard

// 990/991（统计一致性审计轮 + 统计 API 时区标准化轮）投递门。
//
// ★ 与 migration_980_delivery_test.go 同构：把「两条迁移必须真的进投递链」
//   写成可执行断言。2026-11-19 实测：990/991 落地时七处登记面全部缺席
//   （embeddata、runner.go StartupFiles、tsv、main.go go:embed/map、
//   apply-db-revision-sequence.sh files 数组、db-changelog 台账、
//   update_session_summary clobber 链尾），本门钉住其中脚本可断言的面。
//
// ★ 990 特有事实（写进断言，防止未来「看起来能跑」的误判）：
//   它重定义 update_session_summary()，但触发器 trg_update_session_summary
//   已随 v1 族物理退役（890 删表连带删触发器）⇒ 函数体是死代码。
//   clobber 守卫仍要求 990 成为链尾（通道纪律＝链内最后一次定义）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration990StatsConsistencyFixesDelivered(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", "990_stats_consistency_fixes.sql")
	body, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 990 正本失败（投递面断裂）: %v", err)
	}
	// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
	exec := sgStripSQLComments(string(body))
	rawUp := string(body)

	// ① 幂等形态：ADD COLUMN IF NOT EXISTS（内容指纹重放零冲突的前提）。
	if !strings.Contains(exec, "ADD COLUMN IF NOT EXISTS total_credits_charged BIGINT") ||
		!strings.Contains(exec, "ADD COLUMN IF NOT EXISTS avg_ttft_ms INT") ||
		!strings.Contains(exec, "ADD COLUMN IF NOT EXISTS input_cost_usd NUMERIC(20,8)") {
		t.Fatalf("990 缺 ADD COLUMN IF NOT EXISTS 幂等形态 ⇒ 指纹重放通道会 42701/42P16")
	}
	// ② 对账视图在场。
	if !strings.Contains(exec, "CREATE OR REPLACE VIEW stats_consistency_check AS") {
		t.Fatalf("990 缺 stats_consistency_check 对账视图")
	}
	// ③ 锁预算声明（>870 启动迁移纪律；本件含 ALTER TABLE 强锁 DDL）。
	if !strings.Contains(exec, "SET LOCAL lock_timeout") {
		t.Fatalf("990 缺 SET LOCAL lock_timeout ⇒ 部署通道强锁 DDL 无预算护栏")
	}
	// ④ 台账自登记（≥952 纪律）。
	if !strings.Contains(exec, "VALUES ('990',") {
		t.Fatalf("990 缺 schema_migrations 自登记（≥952 纪律）")
	}

	// ⑤ embeddata 副本逐字一致（installer 强制门之外的双保险）。
	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata",
		"startup", "990_stats_consistency_fixes.sql")
	embedBody, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("读 990 embeddata 副本失败: %v", err)
	}
	if string(embedBody) != rawUp {
		t.Fatalf("990 正本与 embeddata 副本不一致（installer 会应用另一份内容）")
	}

	// ⑥ docker 部署通道的序列清单已登记。
	seq, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	// ★ 精确到 files=(...) 的整条条目（★ 只判文件名会命中同名探针行）。
	if !strings.Contains(sgStripShellComments(string(seq)),
		`"$ROOT_DIR/sql/migrations/startup/990_stats_consistency_fixes.sql"`) {
		t.Fatalf("docker 部署通道序列清单缺 990 ⇒ 211 部署不会应用该迁移")
	}
	// ⑦ update_session_summary clobber 链尾（通道纪律：链内最后一次定义）。
	//    ⚠ 触发器已随 v1 族退役（890）⇒ 函数体是死代码；链尾登记是守卫要求，
	//    不是「活库终态体」的语义断言。
	chain := "'update_session_summary|572_session_summary_large_token_ratio.sql|" +
		"563_session_summary_trigger_on_hot.sql|358_session_ownership.sql|" +
		"661_session_summary_token_ratio_reassert.sql|990_stats_consistency_fixes.sql|'"
	if !strings.Contains(sgStripShellComments(string(seq)), chain) {
		t.Fatalf("990 未登记为 update_session_summary clobber 链尾 ⇒ 部署会在应用任何文件之前 abort（exit 5）")
	}
}

func TestMigration991TimezoneStandardizationDelivered(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", "991_stats_timezone_standardization.sql")
	body, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 991 正本失败（投递面断裂）: %v", err)
	}
	// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
	exec := sgStripSQLComments(string(body))
	rawUp := string(body)

	// ① 三个 UTC 日边界函数必须都在（本迁移的全部产物）。
	for _, fn := range []string{
		"CREATE OR REPLACE FUNCTION utc_day_start(ts timestamptz)",
		"CREATE OR REPLACE FUNCTION utc_today()",
		"CREATE OR REPLACE FUNCTION utc_days_ago(n int)",
	} {
		if !strings.Contains(exec, fn) {
			t.Fatalf("991 缺辅助函数: %q", fn)
		}
	}
	// ② 自登记（≥952 纪律）+ 首版 42P01 雷已收口（合并采纳上游字节：
	//    上游以「删引用」收口——原 INSERT INTO migration_metadata 指向不存在的
	//    表，改回 schema_migrations 自登记，不建表；我方原加固是建表方案，
	//    两方案等价消除 42P01，采纳已部署面）。
	if !strings.Contains(exec, "INSERT INTO public.schema_migrations") {
		t.Fatalf("991 缺 schema_migrations 自登记（≥952 纪律）")
	}
	//    防回归：活体 INSERT 不得再指向 migration_metadata（注释提及不算）。
	for _, line := range strings.Split(exec, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(trimmed, "INSERT INTO migration_metadata") {
			t.Fatalf("991 仍有活体 INSERT INTO migration_metadata ⇒ 无表库上 42P01（installer 增量模式每次 boot 撞）")
		}
	}
	// ③ 锁预算声明：**不适用**（上游字节本件纯函数无强锁 DDL，与 992 同口径；
	//    合并前我方加固版带 SET lock_timeout，采纳上游后此处不设断言——
	//    >870 锁预算纪律只对含强锁 DDL 的迁移强制）。
	// ④ 台账自登记（≥952 纪律）。
	if !strings.Contains(exec, "VALUES ('991',") {
		t.Fatalf("991 缺 schema_migrations 自登记（≥952 纪律）")
	}

	// ⑤ embeddata 副本逐字一致。
	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata",
		"startup", "991_stats_timezone_standardization.sql")
	embedBody, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("读 991 embeddata 副本失败: %v", err)
	}
	if string(embedBody) != rawUp {
		t.Fatalf("991 正本与 embeddata 副本不一致（installer 会应用另一份内容）")
	}

	// ⑥ docker 部署通道的序列清单已登记。
	seq, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	// ★ 剥整行 shell 注释 ＋ 精确到 files=(...) 的整条条目。
	if !strings.Contains(sgStripShellComments(string(seq)),
		`"$ROOT_DIR/sql/migrations/startup/991_stats_timezone_standardization.sql"`) {
		t.Fatalf("docker 部署通道序列清单缺 991 ⇒ 211 部署不会应用该迁移")
	}
	// ⑦ 内容指纹重放清单已登记（990/991 在本清单登记前已被手工应用到
	//    252 与本地测试库，台账 sha 为 NULL 遗留记账 ⇒ 必须按指纹收口）。
	if !strings.Contains(sgStripShellComments(string(seq)), "'991_stats_timezone_standardization.sql|") {
		t.Fatalf("991 未登记进 legacy_content_replays 指纹清单 ⇒ 遗留记账永远收不了口")
	}
}
