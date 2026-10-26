package startup

// migration_910_routing_analytics_source_hashjoin_test.go —— 910 的契约门。
//
// # 这道门守的是什么
//
// 910 把 `public.routing_analytics_source` 的父表腿从 907 的
// 「LEFT JOIN LATERAL (... LIMIT 1) 逐行回定位」换成
// 「**普通 LEFT JOIN + partition_date 日界下推**」，
// 修的是 907 形态的**残留成本**：父表腿 ~2.75 万次/刷新 的逐行探针
// （252 实测 Limit/Append 节点 0.366ms x 27,486 loops ≈ 10.1s，占 REFRESH 57%），
// 且探针单次成本随宿主负载漂 100x+（R96 实测 ~3us/次）⇒
// REFRESH 墙钟**乘法式**暴露在宿主负载上。
//
// ★ 结构效果（252 只读实测，2026-10-27，多轮稳定）：
//
//	整计划 buffers 146,423~147,079 → **51,425~52,664（-64%）**；
//	父表腿 133,827 → 38,101+1,239read（details 侧从 27,526 次 x 4 页随机下降
//	变成一次 pkey 范围扫 + Hash Join，Batches: 1 / 3.8MB）。
//	墙钟只作时点（§143.28）：暖态低负载 8,531/7,354ms → 7,183/6,064ms；
//	高负载态 27,397ms → 4,970ms。
//
// ★ 口径等价（252 生产实测，同一事务同一快照）：
//
//	窗口内父表腿双向 EXCEPT ALL（66,838 行）：only_current=**0** / only_candidate=**0**；
//	等价性前提 ①：UNIQUE (tenant_id, request_id, partition_date) 建在分区父表
//	（catalog 实测 session_turn_details_request_id_key）⇒ 普通 JOIN 不增殖
//	（join 后行数 66,838 = 窗口行数）；
//	等价性前提 ②：日界下推被 t.ts 窗口蕴含（错位行实测 = 0）。
//	★ hot 腿**不动**：details_hot 的索引是非唯一 request 索引，唯一键证明
//	只覆盖分区父表 ⇒ LATERAL LIMIT 1 在 hot 腿仍是必需的。
//
// # 视图类迁移最容易出的错，本门各钉一条
//
//	① **形状必须逐字对**：父表腿普通 JOIN + 日界下推 + 三键；hot 腿 LATERAL + LIMIT 1。
//	   父表腿一旦回退到逐行探针 ⇒ LATERAL 计数变 2 ⇒ 本门红（无症状退化：口径不变、只是慢）。
//	② **语义耦合（★ 接棒 907 的同名门）**：迁移里的视图体必须与 Go 侧常量
//	   `db.RoutingAnalyticsSourceViewDDL`（= **启动期执行**的那段文本）**逐字一致**。
//	③ **零 DDL**：本迁移**只换视图体**（907 实测覆盖索引白建的先例）。
//	④ **matview 不需要重建**：CREATE OR REPLACE VIEW **保留 OID** ⇒ 迁移里
//	   **不得**出现 DROP MATERIALIZED VIEW。
//	⑤ **不得回退读 canonical 视图 / v1 底表**（前者是 R95 的根因，后者已退役）。
//	⑥ **锁与超时**：必须带 lock_timeout 与 statement_timeout（903 系同款）。
//	⑦ **幂等**：CREATE OR REPLACE VIEW 本身幂等；重跑必须结果相同而不是报错。
//	⑧ **投递面（7 点）**：installer / 升级序列 / 清单镜像 / 台账缺一 ⇒ 生产库静默跳过。
//
// ★ 迁移文件被**冻结**（sha256 钉死）：改了必须同步台账与本门，
//   且必须重新评估「已应用实例上的行为差异」（本仓纪律：迁移不可变）。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

const (
	mig910Name     = "910_routing_analytics_source_hashjoin.sql"
	mig910DownName = "910_routing_analytics_source_hashjoin.down.sql"

	mig910UpSHA256   = "9aa01a700e2bfbb6ab3e60eb256859cb7c4d863442e3002b2d0e997ff011811f"
	mig910DownSHA256 = "52a87e34a080834f2abb8106816ed383abb926f15db3d3a6463a3b27216c86b2"

	mig910DayRangePred = "dd.partition_date >= (now() - interval '8 days')::date"
)

func mig910Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig910Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// mig910StripComments 剥掉 SQL 行注释与 /* */ 块注释。
//
// ★ 必须剥：迁移头部与视图体注释里写着日界谓词与 "LIMIT 1" 等字面量（作为
//
//	**说明**）⇒ 不剥的话「形状」类断言会命中注释 ⇒ 门恒绿。
func mig910StripComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	joined := strings.Join(out, "\n")
	for {
		i := strings.Index(joined, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(joined[i:], "*/")
		if j < 0 {
			joined = joined[:i]
			break
		}
		joined = joined[:i] + joined[i+j+2:]
	}
	return joined
}

// mig910CheckUpBody 是**检测器**：对剥完注释的 up 文件体逐条判形状。
// ★ 返回问题清单（空 = 合格）⇒ 阳性对照与真文件共用同一个检测器。
func mig910CheckUpBody(clean string) []string {
	var problems []string
	count := func(needle string) int { return strings.Count(clean, needle) }

	if count("CREATE OR REPLACE VIEW routing_analytics_source AS") != 1 {
		problems = append(problems, "up 文件体必须恰好有一条 CREATE OR REPLACE VIEW routing_analytics_source")
	}
	// ★ 910 核心：父表腿必须用普通 LEFT JOIN（LATERAL 只允许 hot 腿那 1 处）。
	if count("LEFT JOIN LATERAL (") != 1 {
		problems = append(problems, "只允许 hot 腿 1 处 LATERAL（父表腿必须是普通 LEFT JOIN；当前 "+itoa(count("LEFT JOIN LATERAL ("))+" 处）")
	}
	if count("LIMIT 1") != 1 {
		problems = append(problems, "只允许 hot 腿 1 处 LIMIT 1（当前 "+itoa(count("LIMIT 1"))+" 处）")
	}
	if !strings.Contains(clean, "LEFT JOIN public.session_turn_details dd") {
		problems = append(problems, "父表腿必须用普通 LEFT JOIN public.session_turn_details dd")
	}
	if !strings.Contains(clean, mig910DayRangePred) {
		problems = append(problems, "父表腿缺日界下推 "+mig910DayRangePred+"（details 侧执行期分区裁剪的前提）")
	}
	for _, want := range []string{
		"FROM public.session_turns_hot t",
		"FROM public.session_turns t",
		"FROM public.session_turn_details_hot dd",
		"dd.tenant_id = t.tenant_id",
		"dd.request_id = t.request_id",
		"dd.partition_date = t.partition_date",
		"dd.client_model::text AS client_model",
		"dd.provider_id::bigint AS provider_id",
		"dd.auto_profile::text AS auto_profile",
		"d.client_model::text AS client_model",
		"d.provider_id::bigint AS provider_id",
		"d.auto_profile::text AS auto_profile",
		"credential_id ~ '^[0-9]+$'",
		"BEGIN;",
		"COMMIT;",
		"SET LOCAL lock_timeout",
		"SET LOCAL statement_timeout",
	} {
		if !strings.Contains(clean, want) {
			problems = append(problems, "up 文件体缺 "+want)
		}
	}
	// ★ 回退读 canonical 视图 = R95 归因的根因 ⇒ 必须判红。
	if strings.Contains(clean, "FROM request_logs_with_current_month") {
		problems = append(problems, "不得回退读 canonical 视图（details 侧会回到 45 万行顺序扫）")
	}
	if strings.Contains(clean, "FROM _request_logs") {
		problems = append(problems, "不得直读 v1 底表")
	}
	// ★ 零 DDL：907 实测覆盖索引白建 ⇒ 迁移里不该有索引。
	if strings.Contains(strings.ToUpper(clean), "CREATE INDEX") {
		problems = append(problems, "本迁移是零 DDL（只换视图体；CREATE OR REPLACE VIEW 保留 OID）")
	}
	// ★ matview 不需要重建（CREATE OR REPLACE VIEW 保留 OID ⇒ 依赖不动）。
	if strings.Contains(strings.ToUpper(clean), "DROP MATERIALIZED VIEW") {
		problems = append(problems, "不得 DROP MATERIALIZED VIEW（CREATE OR REPLACE VIEW 保留 OID ⇒ matview 依赖不动）")
	}
	return problems
}

func TestMigration910_UpBodyShape(t *testing.T) {
	root := mig910Tree(t)
	clean := mig910StripComments(mig910Read(t, root, filepath.Join("sql", "migrations", "startup", mig910Name)))
	if problems := mig910CheckUpBody(clean); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("910 up 文件体：%s", p)
		}
	}
}

// TestMigration910_DetectorRejectsBadShapes 阳性对照：坏样本必须被判红。
//
// ★ 没有它，上面那个检测器若写坏（例如比较了一个永远不出现的串），
//
//	整段会**恒绿** ⇒ 门测不到任何东西。
func TestMigration910_DetectorRejectsBadShapes(t *testing.T) {
	root := mig910Tree(t)
	good := mig910StripComments(mig910Read(t, root, filepath.Join("sql", "migrations", "startup", mig910Name)))
	if problems := mig910CheckUpBody(good); len(problems) != 0 {
		t.Fatalf("阳性对照失败：真文件被判为不合格 —— 检测器坏了：%v", problems)
	}

	cases := []struct {
		name string
		make func() string
	}{
		{"坏样本①（父表腿回退到 LATERAL 逐行探针 ⇒ 907 形态回归）", func() string {
			old := "LEFT JOIN public.session_turn_details dd\n\t\t  ON dd.tenant_id = t.tenant_id\n\t\t AND dd.request_id = t.request_id\n\t\t AND dd.partition_date = t.partition_date\n\t\t AND " + mig910DayRangePred
			new := "LEFT JOIN LATERAL (\n\t\t  SELECT dd.client_model, dd.provider_id, dd.auto_profile\n\t\t  FROM public.session_turn_details dd\n\t\t  WHERE dd.tenant_id = t.tenant_id\n\t\t    AND dd.request_id = t.request_id\n\t\t    AND dd.partition_date = t.partition_date\n\t\t  LIMIT 1\n\t\t) d ON TRUE"
			return strings.ReplaceAll(good, old, new)
		}},
		{"坏样本②（去掉日界下推 ⇒ details 空但 383MB 的 09 分区被全扫）", func() string {
			return strings.ReplaceAll(good, "\n\t\t AND "+mig910DayRangePred, "")
		}},
		{"坏样本③（回退读 canonical 视图）", func() string {
			return strings.ReplaceAll(good, "FROM public.session_turns_hot t", "FROM request_logs_with_current_month t")
		}},
		{"坏样本④（去掉 lock_timeout）", func() string {
			return strings.Replace(good, "SET LOCAL lock_timeout = '60s';", "", 1)
		}},
		{"坏样本⑤（加了一条索引 ⇒ 零 DDL 被破坏）", func() string {
			return good + "\nCREATE INDEX idx_r910_cover ON public.session_turn_details (tenant_id, request_id, partition_date);\n"
		}},
		{"坏样本⑥（details 少取 auto_profile ⇒ 该列静默变 NULL）", func() string {
			return strings.ReplaceAll(good, "dd.auto_profile::text AS auto_profile", "NULL::text AS auto_profile")
		}},
		{"坏样本⑦（去掉 credential_id 数字守卫 ⇒ 22P02）", func() string {
			return strings.ReplaceAll(good, "credential_id ~ '^[0-9]+$'", "credential_id IS NOT NULL")
		}},
		{"坏样本⑧（丢了 join 的一个键 ⇒ 可能增殖行或扫全表）", func() string {
			return strings.ReplaceAll(good, "AND dd.request_id = t.request_id", "")
		}},
	}
	for _, tc := range cases {
		if len(mig910CheckUpBody(tc.make())) == 0 {
			t.Errorf("%s 被判合格 —— 检测器没有鉴别力", tc.name)
		}
	}
}

// TestMigration910_ViewBodyMatchesRuntimeConstant 是**语义耦合门**（接棒 907）。
//
// ★ 迁移（部署期执行）与 Go 常量（启动期执行）各写一份时，任何一侧改动
//
//	忘了另一侧 ⇒ 部署后的形态与新装/重建路径**不一致** ⇒ 难查的行为分叉。
func TestMigration910_ViewBodyMatchesRuntimeConstant(t *testing.T) {
	root := mig910Tree(t)
	up := mig910Read(t, root, filepath.Join("sql", "migrations", "startup", mig910Name))
	// ★ 两侧都按「从 CREATE 到第一个分号」抽取后再比对 ⇒ 前置注释不算差异。
	want := mig910ExtractViewDDL(db.RoutingAnalyticsSourceViewDDL)
	got := mig910ExtractViewDDL(up)
	if got == "" {
		t.Fatal("910 up 文件里找不到 CREATE OR REPLACE VIEW routing_analytics_source 段")
	}
	if got != want {
		t.Errorf("910 的视图体与 db.RoutingAnalyticsSourceViewDDL 不一致（部署路径与运行时路径会分叉）\n--- go ---\n%s\n--- sql ---\n%s", want, got)
	}
}

func mig910ExtractViewDDL(sql string) string {
	const marker = "CREATE OR REPLACE VIEW routing_analytics_source AS"
	i := strings.Index(sql, marker)
	if i < 0 {
		return ""
	}
	rest := sql[i:]
	j := strings.Index(rest, ";")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j+1])
}

// TestMigration910_FilesAreFrozen 迁移文件被 sha256 冻结。
func TestMigration910_FilesAreFrozen(t *testing.T) {
	root := mig910Tree(t)
	for _, c := range []struct{ rel, want string }{
		{filepath.Join("sql", "migrations", "startup", mig910Name), mig910UpSHA256},
		{filepath.Join("sql", "migrations", "startup", mig910DownName), mig910DownSHA256},
	} {
		sum := sha256.Sum256([]byte(mig910Read(t, root, c.rel)))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%s 的 sha256 = %s，期望 %s —— 迁移文件已改（改了必须同步台账与本门）", c.rel, got, c.want)
		}
	}
}

// TestMigration910_DownRestoresLateralShape down 必须把视图体换回 907 的 LATERAL 形态。
//
// ★ 不回退到 canonical（那是 907.down 的职责；907 已 applied 且内容冻结）。
func TestMigration910_DownRestoresLateralShape(t *testing.T) {
	root := mig910Tree(t)
	clean := mig910StripComments(mig910Read(t, root, filepath.Join("sql", "migrations", "startup", mig910DownName)))
	if strings.Count(clean, "LEFT JOIN LATERAL (") != 2 {
		t.Error("down 文件必须把视图体换回 907 形态（两腿 LATERAL 回定位）")
	}
	if strings.Contains(clean, mig910DayRangePred) {
		t.Error("down 文件里不该残留 910 的日界下推")
	}
	if strings.Contains(clean, "FROM request_logs_with_current_month") {
		t.Error("down 文件不该回退到 canonical 形态（应落在 907 形态）")
	}
	if !strings.Contains(clean, "BEGIN;") || !strings.Contains(clean, "COMMIT;") {
		t.Error("down 文件必须是事务性的")
	}
}

// TestMigration910_EmbeddataCopiesAreByteIdentical embeddata 副本必须逐字节一致。
func TestMigration910_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig910Tree(t)
	for _, name := range []string{mig910Name, mig910DownName} {
		canon := mig910Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embed := mig910Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canon != embed {
			t.Errorf("%s 的 embeddata 副本与 canonical **不逐字节一致** —— 两份会各自漂移", name)
		}
	}
}

// TestMigration910_RegisteredAtAllDeliveryPoints 7 点投递面。
func TestMigration910_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig910Tree(t)

	runner := mig910Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig910Name+`"`) {
		t.Errorf("installer StartupFiles 缺 %s", mig910Name)
	}

	mainGo := mig910Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/"+mig910Name) {
		t.Errorf("installer go:embed 缺 %s", mig910Name)
	}
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/"+mig910DownName) {
		t.Errorf("installer go:embed 缺 %s", mig910DownName)
	}
	if !strings.Contains(mainGo, `"startup/`+mig910Name+`"`) {
		t.Errorf("installer embed map 缺 %s", mig910Name)
	}
	if !strings.Contains(mainGo, `"startup/`+mig910DownName+`"`) {
		t.Errorf("installer embed map 缺 %s", mig910DownName)
	}

	seq := mig910Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, mig910Name) {
		t.Errorf("生产升级序列 scripts/apply-db-revision-sequence.sh 缺 %s", mig910Name)
	}

	tsv := mig910Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !regexp.MustCompile(`(?m)^\d+\t` + regexp.QuoteMeta(mig910Name) + `$`).MatchString(tsv) {
		t.Errorf("清单镜像 sql/schema/installed_startup_migrations.tsv 缺 %s", mig910Name)
	}

	changelog := mig910Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, mig910Name) {
		t.Errorf("台账 docs/db-changelog.md 缺 %s", mig910Name)
	}
	if !strings.Contains(changelog, mig910UpSHA256) {
		t.Errorf("台账 docs/db-changelog.md 缺 910 的 sha256（%s）", mig910UpSHA256)
	}
}
