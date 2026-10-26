package startup

// migration_907_routing_analytics_source_lateral_test.go —— 907 的契约门（R96 刷新链轮）。
//
// # 这道门守的是什么
//
// 907 把 `public.routing_analytics_source` 从「读 canonical 宽投影」换成
// 「**会话族两腿 + LEFT JOIN LATERAL (... LIMIT 1) 回定位**」，
// 修的是共享库上**最大的持续慢查询源**：两个 routing matview 的 REFRESH
// 累计 3,181,560 ms ≈ **53 分钟** DB 时间（单次最长 74,331 ms），
// 瓶颈 = `session_turn_details` **45 万行顺序扫**（Buffers shared hit=95,086 ≈ 743 MB）。
//
// ★ 事故形态（252 生产只读实测，2026-10-24）：
//
//	旧体：`SELECT 15 列 FROM request_logs_with_current_month`
//	      ⇒ 该 canonical 视图是「session_turns_hot ∪ session_turns
//	        LEFT JOIN session_turn_details(_hot)」的 118 列投影
//	      ⇒ 规划器只能选 Hash Join ⇒ details 侧顺序扫全部分区
//	        （`Parallel Seq Scan` × 4 = 326,497 + 128,517 + 18 + 18）
//	⇒ 而 source 的 15 列里**只有 3 列**来自 details
//	  （client_model / provider_id / auto_profile），其余 12 列来自 session_turns
//	⇒ ⇒ 「为了 3 列把整个宽表 JOIN 进来」。
//
// ★ 修法效果（252 生产，3 轮交错 A/B 取中位）：
//
//	旧体 4,482.8 ms（最坏一轮冷缓存 20,378.6 ms）｜新体 2,270.8 ms
//	⇒ 中位 **2.0×**、冷缓存轮 **10.6×**。
//
// ★ 口径等价（252 生产实测，同一语句 ⇒ 同一快照）：
//
//	源层双向 EXCEPT ALL（1,110,635 行）：only_old=**0** / only_new=**0**
//	聚合层（matview 输出口径）：c1_only_old=**0** / c1_only_new=**0**
//	逐列类型对拍：15 列列名与类型逐列一致。
//
// # 视图类迁移最容易出的错，本门各钉一条
//
//	① **形状必须逐字对**：LATERAL + LIMIT 1 + 三键 Index Cond + 两条腿。
//	   少一条 ⇒ 回到 Hash Join + 45 万行顺序扫（**口径不变、只是慢** ⇒ 无症状）。
//	② **语义耦合（★ 本门最有价值的一条）**：迁移里的视图体必须与 Go 侧常量
//	   `db.RoutingAnalyticsSourceViewDDL`（= **启动期执行**的那段文本）**逐字一致**
//	   ⇒ 否则「部署路径」与「运行时真源」各写一份、悄悄漂移。
//	③ **零 DDL**：本迁移**只换视图体**。实测另建的覆盖索引
//	   (tenant_id, request_id, partition_date) INCLUDE (...) **没有被规划器选中**
//	   ⇒ 覆盖索引白建 ⇒ 迁移里出现 CREATE INDEX 就是回归。
//	④ **matview 不需要重建**：CREATE OR REPLACE VIEW **保留 OID** ⇒ matview 依赖不动
//	   ⇒ 下一次 REFRESH 自动采用新定义 ⇒ 迁移里**不得**出现 DROP MATERIALIZED VIEW。
//	⑤ **不得回退读 canonical 视图 / v1 底表**（前者是 R95 的根因，后者已退役）。
//	⑥ **锁与超时**：startup 迁移默认在事务里跑 ⇒ 必须带 lock_timeout 与
//	   statement_timeout（共享 PG 服务端 30s 会掐断长语句 ⇒ 57014 击穿后迁移永不记账）。
//	⑦ **幂等**：CREATE OR REPLACE VIEW 本身幂等；重跑必须结果相同而不是报错。
//	⑧ **投递面（7 点）**：只登记在 canonical 目录而没登记在 installer /
//	   升级序列 / 清单镜像 / 台账里 ⇒ 生产库会静默跳过 ⇒「迁移写了但没生效」。
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
	mig907Name     = "907_routing_analytics_source_lateral.sql"
	mig907DownName = "907_routing_analytics_source_lateral.down.sql"

	mig907UpSHA256   = "7db8e5d724ebf9e5af5502b05f66606f4556d6d7c7e93353f718a0316a6b17f6"
	mig907DownSHA256 = "6491443c525af8c0194ae5ac2425c64e798c60659163eceaf66f547cac9bbd11"
)

func mig907Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig907Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// mig907StripComments 剥掉 SQL 行注释与 /* */ 块注释。
//
// ★ 必须剥：迁移头部与视图体里都写着 "request_logs_with_current_month"、
//
//	"LEFT JOIN LATERAL" 等字面量（作为**说明**）⇒ 不剥的话「形状」类断言
//	会命中注释 ⇒ 门恒绿。
func mig907StripComments(s string) string {
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

// mig907CheckUpBody 是**检测器**：对剥完注释的 up 文件体逐条判形状。
// ★ 返回问题清单（空 = 合格）⇒ 阳性对照与真文件共用同一个检测器。
func mig907CheckUpBody(clean string) []string {
	var problems []string
	count := func(needle string) int { return strings.Count(clean, needle) }

	if count("CREATE OR REPLACE VIEW routing_analytics_source AS") != 1 {
		problems = append(problems, "up 文件体必须恰好有一条 CREATE OR REPLACE VIEW routing_analytics_source")
	}
	if count("LEFT JOIN LATERAL (") != 2 {
		problems = append(problems, "两条腿都必须用 LEFT JOIN LATERAL 回定位（当前 "+itoa(count("LEFT JOIN LATERAL ("))+" 处）")
	}
	if count("LIMIT 1") != 2 {
		problems = append(problems, "两条腿的回定位都必须带 LIMIT 1（当前 "+itoa(count("LIMIT 1"))+" 处）")
	}
	for _, want := range []string{
		"FROM public.session_turns_hot t",
		"FROM public.session_turns t",
		"FROM public.session_turn_details_hot dd",
		"FROM public.session_turn_details dd",
		"dd.tenant_id = t.tenant_id",
		"dd.request_id = t.request_id",
		"dd.partition_date = t.partition_date",
		"dd.client_model, dd.provider_id, dd.auto_profile",
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
	// ★ 零 DDL：覆盖索引实测没被规划器选中 ⇒ 迁移里不该有索引。
	if strings.Contains(strings.ToUpper(clean), "CREATE INDEX") {
		problems = append(problems, "本迁移是零 DDL（实测覆盖索引没被规划器选中 ⇒ 白建）")
	}
	// ★ matview 不需要重建（CREATE OR REPLACE VIEW 保留 OID ⇒ 依赖不动）。
	if strings.Contains(strings.ToUpper(clean), "DROP MATERIALIZED VIEW") {
		problems = append(problems, "不得 DROP MATERIALIZED VIEW（CREATE OR REPLACE VIEW 保留 OID ⇒ matview 依赖不动）")
	}
	return problems
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestMigration907_UpBodyShape(t *testing.T) {
	root := mig907Tree(t)
	clean := mig907StripComments(mig907Read(t, root, filepath.Join("sql", "migrations", "startup", mig907Name)))
	if problems := mig907CheckUpBody(clean); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("907 up 文件体：%s", p)
		}
	}
}

// TestMigration907_DetectorRejectsBadShapes 阳性对照：坏样本必须被判红。
//
// ★ 没有它，上面那个检测器若写坏（例如比较了一个永远不出现的串），
//
//	整段会**恒绿** ⇒ 门测不到任何东西。
func TestMigration907_DetectorRejectsBadShapes(t *testing.T) {
	root := mig907Tree(t)
	good := mig907StripComments(mig907Read(t, root, filepath.Join("sql", "migrations", "startup", mig907Name)))
	if problems := mig907CheckUpBody(good); len(problems) != 0 {
		t.Fatalf("阳性对照失败：真文件被判为不合格 —— 检测器坏了：%v", problems)
	}

	cases := []struct {
		name string
		make func() string
	}{
		{"坏样本①（追加第三条 LATERAL 腿）", func() string {
			return good + "\nCREATE OR REPLACE VIEW routing_analytics_source_x AS SELECT 1 LEFT JOIN LATERAL (SELECT 1 LIMIT 1) d ON TRUE;\n"
		}},
		{"坏样本②（去掉 LIMIT 1 ⇒ 回到宽 LEFT JOIN）", func() string {
			return strings.ReplaceAll(good, "LIMIT 1\n", "")
		}},
		{"坏样本③（回退读 canonical 视图）", func() string {
			return strings.ReplaceAll(good, "FROM public.session_turns_hot t", "FROM request_logs_with_current_month t")
		}},
		{"坏样本④（去掉 lock_timeout）", func() string {
			return strings.Replace(good, "SET LOCAL lock_timeout = '60s';", "", 1)
		}},
		{"坏样本⑤（加了一条索引 ⇒ 零 DDL 被破坏）", func() string {
			return good + "\nCREATE INDEX idx_r96_cover ON public.session_turn_details (tenant_id, request_id, partition_date) INCLUDE (client_model);\n"
		}},
		{"坏样本⑥（details 少取 auto_profile ⇒ 该列静默变 NULL）", func() string {
			return strings.ReplaceAll(good, "dd.client_model, dd.provider_id, dd.auto_profile", "dd.client_model, dd.provider_id")
		}},
		{"坏样本⑦（去掉 credential_id 数字守卫 ⇒ 22P02）", func() string {
			return strings.ReplaceAll(good, "credential_id ~ '^[0-9]+$'", "credential_id IS NOT NULL")
		}},
		{"坏样本⑧（丢了回定位的一个键 ⇒ 可能退化成扫全表）", func() string {
			return strings.ReplaceAll(good, "AND dd.partition_date = t.partition_date", "")
		}},
	}
	for _, tc := range cases {
		if len(mig907CheckUpBody(tc.make())) == 0 {
			t.Errorf("%s 被判合格 —— 检测器没有鉴别力", tc.name)
		}
	}
}

// TestMigration907_ViewBodyMatchesRuntimeConstant 是**语义耦合门**。
//
// ★ 迁移（部署期执行）与 Go 常量（启动期执行）各写一份时，任何一侧改动
//
//	忘了另一侧 ⇒ 部署后的形态与新装/重建路径**不一致** ⇒ 难查的行为分叉。
func TestMigration907_ViewBodyMatchesRuntimeConstant(t *testing.T) {
	root := mig907Tree(t)
	up := mig907Read(t, root, filepath.Join("sql", "migrations", "startup", mig907Name))
	// ★ 两侧都按「从 CREATE 到第一个分号」抽取后再比对 ⇒ 前置注释不算差异。
	want := mig907ExtractViewDDL(db.RoutingAnalyticsSourceViewDDL)
	got := mig907ExtractViewDDL(up)
	if got == "" {
		t.Fatal("907 up 文件里找不到 CREATE OR REPLACE VIEW routing_analytics_source 段")
	}
	if got != want {
		t.Errorf("907 的视图体与 db.RoutingAnalyticsSourceViewDDL 不一致（部署路径与运行时路径会分叉）\n--- go ---\n%s\n--- sql ---\n%s", want, got)
	}
}

func mig907ExtractViewDDL(sql string) string {
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

// TestMigration907_FilesAreFrozen 迁移文件被 sha256 冻结。
func TestMigration907_FilesAreFrozen(t *testing.T) {
	root := mig907Tree(t)
	for _, c := range []struct{ rel, want string }{
		{filepath.Join("sql", "migrations", "startup", mig907Name), mig907UpSHA256},
		{filepath.Join("sql", "migrations", "startup", mig907DownName), mig907DownSHA256},
	} {
		sum := sha256.Sum256([]byte(mig907Read(t, root, c.rel)))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%s 的 sha256 = %s，期望 %s —— 迁移文件已改（改了必须同步台账与本门）", c.rel, got, c.want)
		}
	}
}

// TestMigration907_DownRestoresCanonicalShape down 必须把视图体换回 canonical 形态。
func TestMigration907_DownRestoresCanonicalShape(t *testing.T) {
	root := mig907Tree(t)
	clean := mig907StripComments(mig907Read(t, root, filepath.Join("sql", "migrations", "startup", mig907DownName)))
	if !strings.Contains(clean, "FROM request_logs_with_current_month") {
		t.Error("down 文件必须把视图体换回 canonical 形态（FROM request_logs_with_current_month）")
	}
	if strings.Contains(clean, "LEFT JOIN LATERAL") {
		t.Error("down 文件里不该残留 LATERAL 形态")
	}
	if !strings.Contains(clean, "BEGIN;") || !strings.Contains(clean, "COMMIT;") {
		t.Error("down 文件必须是事务性的")
	}
}

// TestMigration907_EmbeddataCopiesAreByteIdentical embeddata 副本必须逐字节一致。
func TestMigration907_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig907Tree(t)
	for _, name := range []string{mig907Name, mig907DownName} {
		canon := mig907Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embed := mig907Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canon != embed {
			t.Errorf("%s 的 embeddata 副本与 canonical **不逐字节一致** —— 两份会各自漂移", name)
		}
	}
}

// TestMigration907_RegisteredAtAllDeliveryPoints 7 点投递面。
func TestMigration907_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig907Tree(t)

	runner := mig907Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig907Name+`"`) {
		t.Errorf("installer StartupFiles 缺 %s", mig907Name)
	}

	mainGo := mig907Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/"+mig907Name) {
		t.Errorf("installer go:embed 缺 %s", mig907Name)
	}
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/"+mig907DownName) {
		t.Errorf("installer go:embed 缺 %s", mig907DownName)
	}
	if !strings.Contains(mainGo, `"startup/`+mig907Name+`"`) {
		t.Errorf("installer embed map 缺 %s", mig907Name)
	}
	if !strings.Contains(mainGo, `"startup/`+mig907DownName+`"`) {
		t.Errorf("installer embed map 缺 %s", mig907DownName)
	}

	seq := mig907Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, mig907Name) {
		t.Errorf("生产升级序列 scripts/apply-db-revision-sequence.sh 缺 %s", mig907Name)
	}

	tsv := mig907Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !regexp.MustCompile(`(?m)^\d+\t` + regexp.QuoteMeta(mig907Name) + `$`).MatchString(tsv) {
		t.Errorf("清单镜像 sql/schema/installed_startup_migrations.tsv 缺 %s", mig907Name)
	}

	changelog := mig907Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, mig907Name) {
		t.Errorf("台账 docs/db-changelog.md 缺 %s", mig907Name)
	}
	if !strings.Contains(changelog, mig907UpSHA256) {
		t.Errorf("台账 docs/db-changelog.md 缺 907 的 sha256（%s）", mig907UpSHA256)
	}
}
