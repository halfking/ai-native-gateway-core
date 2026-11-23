package sqlguard

// 980（PI 统计一致性批判审计轮）投递门：v_error_analysis_stats 必须补齐
// processing/failed/skipped 三个计数列，与 GetStats 新口径逐项对齐。
//
// ★ 为什么需要这道门：视图当前零运行时消费者，「少一列」不会有任何测试
//   变红——但它是 DB 公开真值，口径漂移只会在未来某个读视图的监控上线时
//   爆发。钉住 980 的形状 = 把「两份真值必须同口径」写成可执行断言。
//   （先例：migration_977_delivery_test.go 对 977 的 F2 视图重写同款门。）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration980ViewAlignmentDelivered(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", "980_pi_analysis_stats_view_alignment.sql")
	body, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 980 正本失败（投递面断裂）: %v", err)
	}
	// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
	exec := sgStripSQLComments(string(body))
	rawUp := string(body)

	// ① 视图重建必须是幂等的 DROP+CREATE（可被内容指纹重放）。
	//   ★ 首版用 CREATE OR REPLACE 并对调了 977 的列序 ⇒ 三台部署活体打回
	//   （"cannot change name of view column"）⇒ DROP+CREATE 才允许任意列序。
	if !strings.Contains(exec, "DROP VIEW IF EXISTS public.v_error_analysis_stats") ||
		!strings.Contains(exec, "CREATE VIEW public.v_error_analysis_stats AS") {
		t.Fatalf("980 缺 DROP VIEW IF EXISTS + CREATE VIEW ⇒ 列序对齐会被 PG 拒（首版活体事故形态）")
	}
	// ② 三个新计数列必须在（口径对齐的核心断言）。
	for _, col := range []string{
		"FILTER (WHERE status = 'processing') AS processing_count",
		"FILTER (WHERE status = 'failed') AS failed_count",
		"FILTER (WHERE status = 'skipped') AS skipped_count",
	} {
		if !strings.Contains(exec, col) {
			t.Fatalf("980 视图缺少新计数列: %q（口径未对齐 GetStats）", col)
		}
	}
	// ③ 视图在场守卫（971 未应用的库上跳过而不是 42P01）。
	if !strings.Contains(exec, "to_regclass('public.v_error_analysis_stats') IS NULL") {
		t.Fatalf("980 缺视图在场守卫 ⇒ 未跑 971 的库上迁移会 42P01")
	}
	// ④ 台账自登记。
	if !strings.Contains(exec, "VALUES ('980',") {
		t.Fatalf("980 缺 schema_migrations 自登记（≤952 纪律）")
	}

	// ⑤ embeddata 副本逐字一致（installer 强制门之外的双保险）。
	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata",
		"startup", "980_pi_analysis_stats_view_alignment.sql")
	embedBody, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("读 980 embeddata 副本失败: %v", err)
	}
	if string(embedBody) != rawUp {
		t.Fatalf("980 正本与 embeddata 副本不一致（installer 会应用另一份内容）")
	}

	// ⑥ docker 部署通道的序列清单已登记（211/154 的第六/七处登记面）。
	seq, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	// ★ 精确到 files=(...) 的**整条条目**（★ 只判「文件出现过」⇒ 同名出现在
	//   产物探针/台账行里也会命中 ⇒ ★ 注释掉 files 条目照样绿 ⇒ ★ 实测本轮踩到）。
	if !strings.Contains(sgStripShellComments(string(seq)),
		`"$ROOT_DIR/sql/migrations/startup/980_pi_analysis_stats_view_alignment.sql"`) {
		t.Fatalf("docker 部署通道序列清单缺 980 ⇒ 211 部署不会应用该迁移")
	}
}
