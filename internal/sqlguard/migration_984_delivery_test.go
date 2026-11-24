package sqlguard

// 984（会话宇宙探测误杀修复轮）投递门：会话宇宙谓词加「真实计费豁免」+
// 四物化视图重建 + 15 索引重建。
//
// ★ 为什么需要这道门：本迁移的**失败形态是静默的**——
//
//	① 漏一个投递面（embeddata 副本 / main.go 注册 / StartupFiles / 离线副本 /
//	  docker 通道清单）⇒ **每条通道各自跳过它** ⇒ 功能面看起来「代码都写了」，
//	  而生产上视图定义仍是旧口径 ⇒ **会话统计继续少算 $7,021.91**，
//	  而没有任何报错（997/998/999 三迁移同款形态，2026-11-21 211 生产实测）；
//	② 副本字节不一致 ⇒ installer 通道与 docker 通道跑**不同的内容**
//	  （994 同款教训）；
//	③ 「豁免项」被写成恒真 / 被抹掉 ⇒ 探测会话全放行（口径反向漂移）。
//
// ⇒ 本门把上面三件事写成**可执行断言**（先例：migration_994_delivery_test.go /
//   migration_997_998_999_delivery_test.go）。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	pi984UpFile   = "984_session_universe_cost_aware_probe_scope.sql"
	pi984DownFile = "984_session_universe_cost_aware_probe_scope.down.sql"
)

// pi984CostEscape 是「真实计费豁免」在会话级视图里的**逐字**形态。
//
// ★ 这不是「随便挑个片段」：它是本次修复的**全部语义**——
//
//	「有真实计费 ⇒ 计入；零成本会话 ⇒ 沿用旧排除」。
//	少了它 ⇒ 984 变成空转迁移（视图重建了但口径没变 ⇒ 费用差照旧）。
const pi984CostEscape = "COALESCE(ss.total_cost_usd, 0) > 0"

// pi984OldExclusion 是零成本会话的旧排除项（必须**同时**在）。
//
// ★ 「豁免项在 + 旧排除项在」两条同时成立才说明口径是【收窄】而不是【全放行】。
const pi984OldExclusion = "NOT COALESCE(s.is_probe, false)"

func TestMigration984CostAwareProbeScopeDelivered(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", pi984UpFile)
	body, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 984 正本失败（投递面断裂）: %v", err)
	}
	exec := string(body)

	// ① 语义：豁免项在 + 旧排除项在 + 请求级按 rl.cost_usd 豁免。
	for _, frag := range []string{
		pi984CostEscape,
		pi984OldExclusion,
		"AND NOT session_analytics_is_probe(",
		"COALESCE(rl.cost_usd, 0) = 0",
	} {
		if !strings.Contains(exec, frag) {
			t.Fatalf("984 缺关键片段 %q ⇒ 口径不是本次修复的形态（豁免项被抹掉 ⇒ 探测会话全放行）", frag)
		}
	}
	// ★ 反向断言：豁免项**不得**出现在 down 里（回滚 = 回到旧口径）。
	down := filepath.Join("..", "..", "sql", "migrations", "startup", pi984DownFile)
	downBody, err := os.ReadFile(down)
	if err != nil {
		t.Fatalf("读 984 down 迁移失败: %v", err)
	}
	downExec := string(downBody)
	if strings.Contains(downExec, pi984CostEscape) {
		t.Fatalf("984 的 down 里出现了豁免项 ⇒ 回滚不会真正回到 994 口径")
	}
	if !strings.Contains(downExec, pi984OldExclusion) {
		t.Fatalf("984 的 down 里丢了旧排除项 ⇒ 回滚后的口径既不是 994 也不是 984")
	}

	// ② 四视图 DROP + CREATE IF NOT EXISTS（两态自愈：视图缺失 / 换口径）。
	for _, view := range []string{
		"user_session_aggregates",
		"project_task_aggregates",
		"agent_usage_aggregates",
		"model_usage_aggregates",
	} {
		if !strings.Contains(exec, "DROP MATERIALIZED VIEW IF EXISTS public."+view) ||
			!strings.Contains(exec, "CREATE MATERIALIZED VIEW IF NOT EXISTS public."+view) {
			t.Fatalf("984 缺 %s 的 DROP+CREATE IF NOT EXISTS 重建形态", view)
		}
	}

	// ③ 15 个索引（4 视图的全量）必须重建 —— DROP MV 时索引随视图一起消失。
	for _, idx := range []string{
		"idx_user_session_agg_pk", "idx_user_session_agg_date", "idx_user_session_agg_tenant", "idx_user_session_agg_owner",
		"idx_project_task_agg_pk", "idx_project_task_agg_owner", "idx_project_task_agg_project", "idx_project_task_agg_tenant",
		"idx_agent_usage_agg_pk", "idx_agent_usage_agg_agent", "idx_agent_usage_agg_tenant",
		"idx_model_usage_agg_pk", "idx_model_usage_agg_model", "idx_model_usage_agg_provider", "idx_model_usage_agg_tenant",
	} {
		if !strings.Contains(exec, "CREATE UNIQUE INDEX IF NOT EXISTS "+idx) &&
			!strings.Contains(exec, "CREATE INDEX IF NOT EXISTS "+idx) {
			t.Fatalf("984 缺索引 %s ⇒ DROP MV 后索引永久消失（REFRESH CONCURRENTLY 与查询性能同时受损）", idx)
		}
	}

	// ④ 超时/锁守卫（会话级 SET；docker 通道是非事务流式 psql ⇒ SET LOCAL 静默失效）。
	for _, guard := range []string{
		"SET lock_timeout = '5s';",
		"SET statement_timeout = '30min';",
		"SET max_parallel_workers_per_gather = 0;",
	} {
		if !strings.Contains(exec, guard) {
			t.Fatalf("984 缺守卫 %s", guard)
		}
	}
	if strings.Contains(exec, "SET LOCAL ") {
		t.Fatalf("984 用了 SET LOCAL ⇒ docker 流式通道上静默失效（981/994 实测先例）")
	}

	// ⑤ 台账自登记（≥952 双账本纪律）+ down 的台账 DELETE 对偶。
	if !strings.Contains(exec, "VALUES ('984',") ||
		!strings.Contains(exec, "INSERT INTO public.schema_migrations (version, description)") ||
		!strings.Contains(exec, "ON CONFLICT (version) DO UPDATE") {
		t.Fatalf("984 缺 schema_migrations 自登记段（应用后运行时台账无行 ⇒ 记账链断层）")
	}
	if !strings.Contains(downExec, "DELETE FROM public.schema_migrations WHERE version = '984'") {
		t.Fatalf("984 down 缺台账 DELETE（与 up 自登记对偶）")
	}

	// ⑥ 四份副本逐字一致（canonical / embeddata ×2 / installer offline ×2）。
	for _, pair := range []struct{ path, want string }{
		{filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", pi984UpFile), exec},
		{filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", pi984DownFile), downExec},
		{filepath.Join("..", "..", "installer", "migrations", strings.TrimSuffix(pi984UpFile, ".sql"), "up.sql"), exec},
		{filepath.Join("..", "..", "installer", "migrations", strings.TrimSuffix(pi984UpFile, ".sql"), "down.sql"), downExec},
	} {
		got, err := os.ReadFile(pair.path)
		if err != nil {
			t.Fatalf("读副本失败 %s: %v", pair.path, err)
		}
		if string(got) != pair.want {
			t.Fatalf("984 正本与副本不一致：%s（投递分裂 ⇒ 两条通道跑不同内容）", pair.path)
		}
	}

	// ⑦ installer 注册面（go:embed ×2 + embeddedSQLFiles map ×2）。
	mainRaw, err := os.ReadFile(filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "main.go"))
	if err != nil {
		t.Fatalf("读 installer main.go 失败: %v", err)
	}
	main := string(mainRaw)
	for _, frag := range []string{
		"//go:embed embeddata/startup/" + pi984UpFile,
		"//go:embed embeddata/startup/" + pi984DownFile,
		`"startup/` + pi984UpFile + `":`,
		`"startup/` + pi984DownFile + `":`,
	} {
		if !strings.Contains(main, frag) {
			t.Fatalf("installer main.go 缺 984 注册面: %q", frag)
		}
	}

	// ⑧ dbinit StartupFiles（新装库跑该迁移）。
	runnerRaw, err := os.ReadFile(filepath.Join("..", "..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("读 runner.go 失败: %v", err)
	}
	if !strings.Contains(string(runnerRaw), `"`+pi984UpFile+`"`) {
		t.Fatalf("dbinit runner.go StartupFiles 缺 984 ⇒ 全新安装的库拿不到新口径")
	}

	// ⑨ TSV 台账镜像（由 StartupFiles 派生）。
	// ★ 2026-11-24 12h 审计轮：原断言钉死序号本身（`394\t984_…`）⇒ 序号随清单位次
	//   变化就会误红（方向安全但脆）。改为「按文件名查找该行 + 行内序号列自洽」：
	//   只要 984 行在场、第二列是文件名、第一列是纯数字序号即通过，不再绑定具体数值。
	tsvRaw, err := os.ReadFile(filepath.Join("..", "..", "sql", "schema", "installed_startup_migrations.tsv"))
	if err != nil {
		t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
	}
	found984Row := false
	for _, line := range strings.Split(string(tsvRaw), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) >= 2 && fields[1] == pi984UpFile {
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(fields[0]) {
				t.Fatalf("tsv 的 984 行序号列非纯数字: %q", line)
			}
			found984Row = true
			break
		}
	}
	if !found984Row {
		t.Fatalf("tsv 缺 984 登记行（第二列应为 %s）", pi984UpFile)
	}

	// ⑩ docker 部署通道：files=() 条目 + 产物探针。
	seqRaw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	seq := string(seqRaw)
	if !strings.Contains(seq, `"$ROOT_DIR/sql/migrations/startup/`+pi984UpFile+`"`) {
		t.Fatalf("docker 部署通道 files=() 缺 984 ⇒ 211/252 部署不会应用该迁移")
	}
	if !strings.Contains(seq, `ledger_artifact_probe "`+pi984UpFile+`"`) {
		t.Fatalf("docker 通道缺 984 产物探针 ⇒ 「投了没建成」不可判")
	}

	// ⑪ 台账登记行（docs/db-changelog.md）必须带**实测** sha。
	//    ★ 台账 sha 与磁盘不一致 ⇒ verify-migration-checksums.sh 会硬拒部署。
	sum := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(sum[:])
	regRaw, err := os.ReadFile(filepath.Join("..", "..", "docs", "db-changelog.md"))
	if err != nil {
		t.Fatalf("读 db-changelog.md 失败: %v", err)
	}
	if !strings.Contains(string(regRaw), "`"+wantSHA+"`") {
		t.Fatalf("db-changelog.md 缺 984 的实测 sha %s（台账与磁盘不一致 ⇒ checksum 闸门硬拒部署）", wantSHA)
	}
}
