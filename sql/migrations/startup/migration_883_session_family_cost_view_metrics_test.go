package startup

// migration_883_session_family_cost_view_metrics_test.go —— 迁移 883 的契约门。
//
// # 这道门守的是什么
//
// 883 把 auto-route 成本仪表盘的**两个活指标**子查询从 v1 冻结族
// （`_request_logs`）切到会话族：
//
//   · customer_cost_view.active_concurrent  —— 5 分钟窗的实时并发数
//   · model_cost_per_task_view.avg_latency_ms —— 7 天窗的平均延迟
//
// 这类「读源切换」最容易出的错有四个，本门逐条钉住：
//
//	① **切了迁移、切了正典、忘了 baseline**（或反之）⇒ 新装环境与存量环境
//	   形状不同。本门钉：canonical 视图文件、正典 schema dump、baseline schema
//	   dump 三处**都**不读 v1、都读会话族；且两份 01-schema.sql 逐字节相同
//	   （deploy/sql/verify-migration.sh 会 diff 它们）。
//	② **只改了 canonical，没改 installer embeddata** ⇒ 装出来的库与仓库分叉。
//	③ **迁移没进部署序列** ⇒ 任何环境都不会执行它。本门钉三处登记：
//	   installer 的 embed map、dbinit 的 StartupFiles、部署通道的 files=()。
//	④ **类型差异被漏掉**（本轮真实踩过）：会话族 `api_key_id` 是 text、
//	   `api_key_model_cost.api_key_id` 是 integer ⇒ 直接比较报
//	   `operator does not exist: text = integer`，视图**建不出来**。
//
// 行为侧的真库门在 admin 包：
// `admin/auto_route_cost_views_session_source_test.go`
// （造夹具证明指标真的取到会话族的值，并做过变异验证）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mig883Name     = "883_session_family_cost_view_metrics.sql"
	mig883DownName = "883_session_family_cost_view_metrics.down.sql"
)

func mig883Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// <root>/sql/migrations/startup → 上溯三级到 <root>
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig883Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// mig883ViewBody 取出文件里某条 CREATE OR REPLACE VIEW 的定义体。
func mig883ViewBody(t *testing.T, content, viewName string) string {
	t.Helper()
	marker := "CREATE OR REPLACE VIEW public." + viewName
	start := strings.Index(content, marker)
	if start < 0 {
		t.Fatalf("未找到 %s 的定义体", viewName)
	}
	rest := content[start:]
	end := strings.Index(rest, ";\n")
	if end < 0 {
		t.Fatalf("%s 的定义体没有以 ';' 结束", viewName)
	}
	return rest[:end]
}

// TestMigration883_ViewBodiesReadSessionFamily 钉 ①（迁移体本身）。
func TestMigration883_ViewBodiesReadSessionFamily(t *testing.T) {
	root := mig883Tree(t)
	up := mig883Read(t, root, filepath.Join("sql", "migrations", "startup", mig883Name))

	for _, viewName := range []string{"customer_cost_view", "model_cost_per_task_view"} {
		body := mig883ViewBody(t, up, viewName)

		// v1 母表读必须消失（两代物理名都算：860 之前 request_logs、之后 _request_logs）
		for _, v1 := range []string{"request_logs rl", "_request_logs rl", "FROM public.request_logs", "FROM public._request_logs"} {
			if strings.Contains(body, v1) {
				t.Errorf("%s 的定义体仍读 v1 族（%q）—— 活指标读冻结族 ⇒ 恒 0 / 恒 NULL，且无错误信号", viewName, v1)
			}
		}

		// 会话族两腿必须在位
		if !strings.Contains(body, "session_turns_hot") {
			t.Errorf("%s 的定义体没有 session_turns_hot 腿", viewName)
		}
	}

	// 5 分钟窗用 hot 单腿（在线写路径只写 hot ⇒ 完整且无重复）
	ccBody := mig883ViewBody(t, up, "customer_cost_view")
	if !strings.Contains(ccBody, "FROM public.session_turns_hot st") {
		t.Errorf("customer_cost_view 的 5 分钟窗必须用 hot 单腿（写路径只写 hot，晋级延迟远大于 5 分钟）")
	}
	if strings.Contains(ccBody, "session_turns st_par") {
		t.Errorf("customer_cost_view 的 5 分钟窗不应带 parent 腿（会引入不必要的父表分区扫描）")
	}

	// 7 天窗用 hot ∪ parent 两条腿
	mcBody := mig883ViewBody(t, up, "model_cost_per_task_view")
	for _, want := range []string{
		"FROM public.session_turns_hot st_hot",
		"UNION ALL",
		"FROM public.session_turns st_par",
	} {
		if !strings.Contains(mcBody, want) {
			t.Errorf("model_cost_per_task_view 的 7 天窗缺少 %q —— 该窗口跨过 hot 保留期，必须带 parent 腿", want)
		}
	}

	// 匹配键必须是**上游原始模型** raw_model_name（v1 outbound_model 的对应物），
	// 不是客户端模型 model
	if !strings.Contains(mcBody, "st.raw_model_name = mcp.raw_model") {
		t.Errorf("model_cost_per_task_view 必须按 raw_model_name（上游原始模型）匹配，不能用 model（客户端模型）")
	}
	if strings.Contains(mcBody, "st.model = mcp.raw_model") {
		t.Errorf("model_cost_per_task_view 的匹配键被换成了客户端模型 model —— 会接到错误的键空间")
	}

	// 类型差异：会话族 api_key_id 是 text，聚合表是 integer ⇒ 必须显式 ::text
	if !strings.Contains(ccBody, "st.api_key_id = akmc.api_key_id::text") {
		t.Errorf("customer_cost_view 必须显式 ::text（会话族 api_key_id 是 text、api_key_model_cost.api_key_id 是 integer）")
	}
}

// TestMigration883_DownRestoresV1Bodies 钉 ②（可回滚，且回滚体确实读 v1）。
func TestMigration883_DownRestoresV1Bodies(t *testing.T) {
	root := mig883Tree(t)
	down := mig883Read(t, root, filepath.Join("sql", "migrations", "startup", mig883DownName))

	for _, viewName := range []string{"customer_cost_view", "model_cost_per_task_view"} {
		if !strings.Contains(down, "CREATE OR REPLACE VIEW public."+viewName) {
			t.Errorf("down 缺少 %s 的还原定义体", viewName)
		}
	}
	if !strings.Contains(down, "FROM public._request_logs rl") {
		t.Errorf("down 必须把两个子查询还原成读 v1 母表（860 之后的物理名）")
	}
}

// TestMigration883_CanonicalAndBaselineInSync 钉 ① 的另一半：
// canonical 视图文件 + 正典 schema dump + baseline schema dump 三处都读会话族，
// 且两份 01-schema.sql 逐字节相同（deploy/sql/verify-migration.sh 会 diff）。
func TestMigration883_CanonicalAndBaselineInSync(t *testing.T) {
	root := mig883Tree(t)

	files := []struct {
		rel string
	}{
		{filepath.Join("sql", "objects", "views", "customer_cost_view.sql")},
		{filepath.Join("sql", "objects", "views", "model_cost_per_task_view.sql")},
		{filepath.Join("sql", "schema", "01-schema.sql")},
		{filepath.Join("deploy", "sql", "schemas", "baseline", "01-schema.sql")},
	}
	for _, f := range files {
		content := mig883Read(t, root, f.rel)
		if strings.Contains(content, "FROM public._request_logs rl") {
			t.Errorf("%s 仍读 v1 族（_request_logs）—— 新装环境会拿到与存量环境不同的视图形状", f.rel)
		}
		if strings.Contains(content, "FROM public.request_logs rl") {
			t.Errorf("%s 仍读 v1 族（request_logs）", f.rel)
		}
		if !strings.Contains(content, "session_turns_hot") {
			t.Errorf("%s 没有会话族读源", f.rel)
		}
	}

	// 两份 01-schema.sql 必须逐字节相同
	canonical := mig883Read(t, root, filepath.Join("sql", "schema", "01-schema.sql"))
	baseline := mig883Read(t, root, filepath.Join("deploy", "sql", "schemas", "baseline", "01-schema.sql"))
	if canonical != baseline {
		t.Errorf("sql/schema/01-schema.sql 与 deploy/sql/schemas/baseline/01-schema.sql 不一致 —— " +
			"deploy/sql/verify-migration.sh 会 diff 它们，新装与存量环境会分叉")
	}
}

// TestMigration883_EmbeddataCopyIdentical 钉 ②：两份字节一致。
func TestMigration883_EmbeddataCopyIdentical(t *testing.T) {
	root := mig883Tree(t)
	for _, name := range []string{mig883Name, mig883DownName} {
		canonical := mig883Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig883Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 的 canonical 与 installer embeddata 两份不一致 —— 只改一份会让装出来的库与仓库分叉", name)
		}
	}
}

// TestMigration883_RegisteredInDeploymentSequence 钉 ③：迁移必须真的进得了序列。
//
// 「写了迁移但没登记」⇒ 任何环境都不会执行它 ⇒ 存量环境的视图形状与新装
// 环境分叉，而且**没有任何报错**。
func TestMigration883_RegisteredInDeploymentSequence(t *testing.T) {
	root := mig883Tree(t)

	// ① installer 的 embed map
	mainGo := mig883Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(mainGo, "embeddata/startup/"+mig883Name) {
		t.Errorf("installer main.go 缺少 883 的 go:embed 指令")
	}
	if !strings.Contains(mainGo, `"startup/`+mig883Name+`"`) {
		t.Errorf("installer main.go 的 startup map 缺少 883 的条目")
	}

	// ② dbinit 的 StartupFiles
	runner := mig883Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, mig883Name) {
		t.Errorf("installer dbinit runner.go 的 StartupFiles 缺少 883")
	}

	// ③ 部署通道的 files=()
	channel := mig883Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(channel, "sql/migrations/startup/"+mig883Name) {
		t.Errorf("部署通道 apply-db-revision-sequence.sh 的 files=() 缺少 883")
	}
}
