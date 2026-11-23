package sqlguard

// 994（会话分析闭环审计轮）投递门：owner 键统一双源 + 四视图重建自愈 + 关闭路径补列。
//
// ★ 为什么需要这道门：994 的三个交付面全是「坏了没人红」的形状——
//   ① owner 键单源→双源：users 页与 overview/user-ranking 两套身份口径并存，
//     数字对不上是静默的；
//   ② 252 生产「台账记了 applied、四视图全缺」：刷新每轮失败无告警（本轮同轮
//     补接），端点族 42P01；statement_timeout 守卫缺了会在共享 PG 上重演 981
//     半途而废；
//   ③ stop_reason 列：两个写入方引用不存在的列 ⇒ 42703（904 头注登记的根因）。
//   钉住 994 的形状 = 把这三件事写成可执行断言（先例：migration_980_delivery_test.go）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration994OwnerKeyAndClosePathDelivered(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", "994_session_analytics_owner_key_and_close_path.sql")
	body, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 994 正本失败（投递面断裂）: %v", err)
	}
	// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
	exec := sgStripSQLComments(string(body))
	rawUp := string(body)

	// ① 视图重建必须是 DROP + CREATE IF NOT EXISTS（252 上视图不存在时 CREATE 生效；
	//   211 上先 DROP 再建 ⇒ 两态自愈）。
	if !strings.Contains(exec, "DROP MATERIALIZED VIEW IF EXISTS public.user_session_aggregates") ||
		!strings.Contains(exec, "CREATE MATERIALIZED VIEW IF NOT EXISTS public.user_session_aggregates") ||
		!strings.Contains(exec, "DROP MATERIALIZED VIEW IF EXISTS public.model_usage_aggregates") ||
		!strings.Contains(exec, "CREATE MATERIALIZED VIEW IF NOT EXISTS public.model_usage_aggregates") {
		t.Fatalf("994 缺 DROP+CREATE IF NOT EXISTS 重建形态 ⇒ 无法同时自愈「视图缺失」与「换键」两态")
	}

	// ② owner 键必须 = COALESCE 双源（三个会话级视图 WHERE/GROUP BY/user_count 三处）。
	ownerExpr := "NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '')"
	if got := strings.Count(exec, ownerExpr); got < 5 {
		t.Fatalf("994 owner 双源键出现 %d 次（WHERE/GROUP BY×3/user_count 预期 ≥5）⇒ 键未统一", got)
	}
	// 请求级 model 视图保持 981 的 rl 双源键（逐字同族）。
	if !strings.Contains(exec, "COUNT(DISTINCT NULLIF(COALESCE(rl.owner_user, rl.api_key_owner_user), ''))") {
		t.Fatalf("994 model_usage_aggregates 丢掉了 981 的 rl 双源键")
	}

	// ③ end_user_count 列 + FILTER 守卫（users 列表换锚后的 API 契约）。
	if !strings.Contains(exec, "AS end_user_count") ||
		!strings.Contains(exec, "COUNT(DISTINCT sd.end_user_id) FILTER (") {
		t.Fatalf("994 user_session_aggregates 缺 end_user_count 聚合列 ⇒ users 列表契约断")
	}

	// ④ statement_timeout 守卫：视图级 SELECT 在 252 实测 90s~197s，
	//   共享 PG 服务端默认 30s 会掐断 ⇒ 981 半途而废的机制。SET（会话级）
	//   而不是 SET LOCAL（docker 流式通道上后者静默失效——981 实测先例）。
	if !strings.Contains(exec, "SET statement_timeout = '30min'") {
		t.Fatalf("994 缺 statement_timeout=30min 守卫 ⇒ 共享 PG 上重建必然再被 30s 掐断")
	}
	if strings.Contains(exec, "SET LOCAL statement_timeout") {
		t.Fatalf("994 用了 SET LOCAL ⇒ docker 通道上静默失效（981 lock_timeout 实测先例）")
	}
	if !strings.Contains(exec, "SET lock_timeout = '5s'") {
		t.Fatalf("994 缺 lock_timeout 守卫（§44 纪律）")
	}

	// ⑤ stop_reason 列 + 台账自登记。
	if !strings.Contains(exec, "ADD COLUMN IF NOT EXISTS stop_reason text") {
		t.Fatalf("994 缺 session_dim.stop_reason 补列 ⇒ 关闭路径 42703 根因未修")
	}
	if !strings.Contains(exec, "VALUES ('994',") {
		t.Fatalf("994 缺 schema_migrations 自登记（≤952 纪律）")
	}

	// ⑥ 三份 SQL 副本逐字一致（canonical / embeddata / installer up）。
	embed := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata",
		"startup", "994_session_analytics_owner_key_and_close_path.sql")
	embedBody, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("读 994 embeddata 副本失败: %v", err)
	}
	if string(embedBody) != rawUp {
		t.Fatalf("994 正本与 embeddata 副本不一致（installer 会应用另一份内容）")
	}
	inst := filepath.Join("..", "..", "installer", "migrations",
		"994_session_analytics_owner_key_and_close_path", "up.sql")
	instBody, err := os.ReadFile(inst)
	if err != nil {
		t.Fatalf("读 994 installer/migrations 副本失败: %v", err)
	}
	if string(instBody) != rawUp {
		t.Fatalf("994 正本与 installer/migrations 副本不一致")
	}

	// ⑦ installer 注册面（go:embed var + embeddedSQLFiles map）与 runner 清单。
	mg, err := os.ReadFile(filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "main.go"))
	if err != nil {
		t.Fatalf("读 installer main.go 失败: %v", err)
	}
	mgs := sgStripGoCommentsKeepEmbed(string(mg))
	for _, want := range []string{
		"embeddata/startup/994_session_analytics_owner_key_and_close_path.sql",
		"\"startup/994_session_analytics_owner_key_and_close_path.sql\":",
		"\"startup/994_session_analytics_owner_key_and_close_path.down.sql\":",
	} {
		if !strings.Contains(mgs, want) {
			t.Fatalf("installer main.go 缺 994 注册面: %q（embed 同步门同型缺口——992 首提交先例）", want)
		}
	}
	runner, err := os.ReadFile(filepath.Join("..", "..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("读 runner.go 失败: %v", err)
	}
	if !strings.Contains(string(runner), `"994_session_analytics_owner_key_and_close_path.sql"`) {
		t.Fatalf("dbinit runner.go StartupFiles 缺 994 ⇒ installer 投递面断裂")
	}

	// ⑧ docker 部署通道的序列清单 + 产物探针（211/154 的第六/七处登记面）。
	seq, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	seqs := string(seq)
	if !strings.Contains(seqs, "994_session_analytics_owner_key_and_close_path.sql") {
		t.Fatalf("docker 部署通道序列清单缺 994 ⇒ 154/245 部署不会应用该迁移（979 首版先例）")
	}
	if !strings.Contains(seqs, `ledger_artifact_probe "994_session_analytics_owner_key_and_close_path.sql"`) {
		t.Fatalf("docker 通道缺 994 产物探针 ⇒ 「投了没建成」不可判")
	}
}
