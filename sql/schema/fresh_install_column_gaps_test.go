package schema

// fresh_install_column_gaps_test.go
//
// 「全新安装缺列族」的门。
//
// # 这道门补的是哪个盲点
//
// 2026-10-21（R60）实测：installer 的全新安装路径
// （00-prereqs → 01-schema 基线 → 02-seed → StartupFiles 登记的 startup 迁移）
// 出来的库，**比生产少 23 个列**（见 fresh_install_column_gaps.tsv）。
// 其中 provider_models.source 缺了 ⇒ **全新安装连库都开不了**：
// db.Open 的 ensure asr catalog seed 直接 42703
// （column "source" of relation "provider_models" does not exist），
// 实测在 db::TestEnsureAsrCatalogSeed_RealDB 与
// sql-migrations-startup::TestMigration715FreshChainApplyMigrations 两条真库门上复现。
//
// ★ 既有门为什么看不见这一类：
//   - sql/schema/startup_known_gaps.tsv 那道棘轮只管「**已登记**的迁移在全新安装上
//     失败」——「文件躺在 sql/migrations/startup/ 里，却从未被登记」对它**完全隐形**
//     （它数的是 failed=0，不是 missing=0）。
//   - 既有那两道真库门（ASR seed / 715 fresh chain）只能看见**一个实例**
//     （provider_models.source），看不见这一类。
//
// ⇒ 本门把「类」变成可检的：扫**未登记**的 startup 文件，看它们加的列在
// **物理表**上是否缺失；缺 ⇒ 必须出现在登记册里，否则红。
//
// # 为什么判据要用「真库」当 oracle，而不是静态解析基线
//
// 试过：静态解析 01-schema.sql 抽「基线提供了哪些列」⇒ 解析器只抓到 271 张表里的
// 220 张（CREATE TABLE 块的花括号/嵌套括号一多就漏）⇒ 差集全是假阳性。
// ⇒ **判据必须与被测对象在同一层**（AGENTS.md 判据纪律第 1 条）：
// 「全新安装缺哪列」这件事的答案在**真库**里，不在 SQL 文本里。
// 而真库腿的一次性库**就是** installer 那条路径的精确复刻 ⇒ 同一层。
//
// # 三条判定
//
//	A（检新缺口，有牙）：未登记的 startup 文件所加的列，在物理表上缺失 ⇒ 必须在登记册里。
//	B（登记册不过期）：登记册每条必须**此刻仍然缺**。缺了才登记；已修复 ⇒ 红，
//	  提示删掉那一行（棘轮方向：修好一个缺口 = 删一行）。
//	C（形状）：排序、去重、reason 必填、severity 必须是白名单里的值。
//
// # 为什么不需要「登记册非空」这个下限
//
// 因为 A 有牙：只要还有一个未登记的 startup 文件加了缺失列，A 就红 ——
// 与登记册空不空无关。而 A 恒绿恰恰意味着「没有新缺口」，那才是真的没问题。
// ⇒ 真正要证明的是 **A 有牙**，所以有
// TestFreshInstallColumnGapDetectorHasTeeth（纯函数合成数据，能红也能绿）。
//
// # 已知边界（写在前面）
//
//   - A 只扫 sql/migrations/startup/ 里**未登记**的文件 ⇒ domain 通道
//     （sql/migrations/domain/）与 root 通道（sql/migrations/ 根目录）的缺口
//     **检不出来** —— 那部分只靠登记册 + 人工复核。
//   - A 只认**物理表**（relkind ∈ r/p）：加在视图上的列（request_logs 现在是视图，
//     物理表是 _request_logs）不判 ⇒ S4 退役改名那一跳是盲区，已在登记册里标注。
//   - 登记册是「生产 vs 新装」的差集，**不是**「产品实际引用了哪些列」的穷举。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	gapRegistryPath = "fresh_install_column_gaps.tsv"
	gapManifestPath = "installed_startup_migrations.tsv"
	gapStartupDir   = "../migrations/startup"
)

// gapSeverityAllowed 是登记册 severity 列的白名单。
var gapSeverityAllowed = map[string]bool{
	"fatal":  true,
	"high":   true,
	"medium": true,
	"low":    true,
	"benign": true,
}

// gapColumnClaim 是一条「某文件声称给某表加了某列」的声明。
type gapColumnClaim struct {
	Table  string
	Column string
}

// gapRegistryEntry 是登记册的一行。
type gapRegistryEntry struct {
	Table    string
	Column   string
	Severity string
	Reason   string
}

// gapShape 是判定所需的全部事实（纯数据 ⇒ 可用合成数据自检）。
type gapShape struct {
	// registeredFiles = 已登记（= 会被 installer 跑）的 startup 文件名。
	registeredFiles map[string]bool
	// physicalTables = 库里是物理表（relkind ∈ r/p）的关系名。
	physicalTables map[string]bool
	// presentColumns = 表 → 该表已存在的列集合。
	presentColumns map[string]map[string]bool
	// registeredColumns = 登记册里登记的 "table.column"。
	registeredColumns map[string]bool
}

// gapDetection 是检测结果。
type gapDetection struct {
	// UnregisteredGaps = 未登记文件带来的、且登记册没写的缺失列。
	UnregisteredGaps []string
	// StaleEntries = 登记册里已经不再缺的条目（已修复 ⇒ 该删行）。
	StaleEntries []string
}

// detectGaps 是纯函数判定：给定事实，返回「新缺口」与「过期条目」。
//
// ★ 抽成纯函数是为了能给它写自检（能红也能绿）；真库腿只负责把事实喂进来。
func detectGaps(shape gapShape, claimsByFile map[string][]gapColumnClaim, entries []gapRegistryEntry) gapDetection {
	var det gapDetection

	for file, claims := range claimsByFile {
		if shape.registeredFiles[file] {
			continue // 已登记 ⇒ 会跑 ⇒ 不是缺口
		}
		for _, c := range claims {
			if !shape.physicalTables[c.Table] {
				continue // 关系不存在或不是物理表（视图）⇒ 不判
			}
			if cols := shape.presentColumns[c.Table]; cols != nil && cols[c.Column] {
				continue // 列已存在 ⇒ 不是缺口
			}
			key := c.Table + "." + c.Column
			if !shape.registeredColumns[key] {
				det.UnregisteredGaps = append(det.UnregisteredGaps, file+": "+key)
			}
		}
	}

	for _, e := range entries {
		cols := shape.presentColumns[e.Table]
		if cols != nil && cols[e.Column] {
			det.StaleEntries = append(det.StaleEntries, e.Table+"."+e.Column)
		}
	}
	sort.Strings(det.UnregisteredGaps)
	sort.Strings(det.StaleEntries)
	return det
}

// ── SQL 解析 ───────────────────────────────────────────────────────────────

var (
	// alterTableRe 抓 ALTER TABLE 语句。表名可带 schema 限定；IF EXISTS / ONLY
	// 是可选前缀（PG 语法：ALTER TABLE [IF EXISTS] [ONLY] name ...）。
	alterTableRe = regexp.MustCompile(
		`(?is)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*(.*?);`)
	// addColumnRe 抓 ADD COLUMN [IF NOT EXISTS] <col>。
	addColumnRe = regexp.MustCompile(
		`(?i)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([A-Za-z_][A-Za-z0-9_]*)"?`)
)

// gapTableKeywords 是不能当表名的 SQL 关键字（解析器的已知噪声源）。
var gapTableKeywords = map[string]bool{
	"if": true, "exists": true, "only": true, "table": true,
	"not": true, "default": true, "from": true, "column": true,
}

// parseAddColumnClaims 抽出一份 SQL 里所有「ALTER TABLE ... ADD COLUMN」声明。
func parseAddColumnClaims(sqlText string) []gapColumnClaim {
	var out []gapColumnClaim
	for _, m := range alterTableRe.FindAllStringSubmatch(sqlText, -1) {
		table := strings.ToLower(strings.Trim(m[1], `"`))
		if i := strings.LastIndex(table, "."); i >= 0 {
			table = table[i+1:]
		}
		if gapTableKeywords[table] {
			continue
		}
		for _, c := range addColumnRe.FindAllStringSubmatch(m[2], -1) {
			col := strings.ToLower(strings.Trim(c[1], `"`))
			if gapTableKeywords[col] {
				continue
			}
			out = append(out, gapColumnClaim{Table: table, Column: col})
		}
	}
	return out
}

// ── 读取事实 ───────────────────────────────────────────────────────────────

// readGapManifest 读已登记迁移登记册（installed_startup_migrations.tsv）。
// 数据行格式是 <index>\t<文件名> ⇒ 文件名在**第 1 列**，不是第 0 列。
//
// ⚠ 这里踩过坑：第一版取了第 0 列（拿到的是序号）⇒ 已登记集合全空 ⇒
// 「未登记文件」变成了全部文件 ⇒ 门报了 84 条假缺口。
// ⇒ 登记册的格式本身也是判据的一部分，改格式必须同步改这里。
func readGapManifest(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(gapManifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", gapManifestPath, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := []string{}
		for _, f := range strings.Split(line, "\t") {
			if f = strings.TrimSpace(f); f != "" {
				fields = append(fields, f)
			}
		}
		if len(fields) == 0 {
			continue
		}
		out[fields[len(fields)-1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s 里没有读到任何已登记迁移 —— 判据的输入为空，等于门恒绿", gapManifestPath)
	}
	return out
}

// readGapRegistry 读缺列登记册。
func readGapRegistry(t *testing.T) []gapRegistryEntry {
	t.Helper()
	b, err := os.ReadFile(gapRegistryPath)
	if err != nil {
		t.Fatalf("read %s: %v", gapRegistryPath, err)
	}
	var out []gapRegistryEntry
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			t.Errorf("%s:%d 字段数 = %d，期望 >= 4（table/column/severity/reason）", gapRegistryPath, i+1, len(parts))
			continue
		}
		out = append(out, gapRegistryEntry{
			Table:    strings.TrimSpace(parts[0]),
			Column:   strings.TrimSpace(parts[1]),
			Severity: strings.TrimSpace(parts[2]),
			Reason:   strings.TrimSpace(parts[3]),
		})
	}
	return out
}

// readUnregisteredClaims 扫 startup 目录里**未登记**的文件，抽出它们的 ADD COLUMN 声明。
func readUnregisteredClaims(t *testing.T, registered map[string]bool) map[string][]gapColumnClaim {
	t.Helper()
	entries, err := os.ReadDir(gapStartupDir)
	if err != nil {
		t.Fatalf("read %s: %v", gapStartupDir, err)
	}
	out := map[string][]gapColumnClaim{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		if registered[name] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(gapStartupDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		claims := parseAddColumnClaims(string(b))
		if len(claims) > 0 {
			out[name] = claims
		}
	}
	return out
}

// readDbShape 从库里取「物理表」与「已存在的列」。
func readDbShape(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (map[string]bool, map[string]map[string]bool) {
	t.Helper()
	physical := map[string]bool{}
	rows, err := pool.Query(ctx, `
		SELECT c.relname, c.relkind::text
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'`)
	if err != nil {
		t.Fatalf("query relations: %v", err)
	}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			t.Fatalf("scan relation: %v", err)
		}
		if kind == "r" || kind == "p" {
			physical[strings.ToLower(name)] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("relations: %v", err)
	}

	present := map[string]map[string]bool{}
	crows, err := pool.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'`)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	for crows.Next() {
		var table, col string
		if err := crows.Scan(&table, &col); err != nil {
			crows.Close()
			t.Fatalf("scan column: %v", err)
		}
		tl := strings.ToLower(table)
		if present[tl] == nil {
			present[tl] = map[string]bool{}
		}
		present[tl][strings.ToLower(col)] = true
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		t.Fatalf("columns: %v", err)
	}
	return physical, present
}

// ── 门 C：登记册形状（无需数据库） ─────────────────────────────────────────

func TestFreshInstallColumnGapRegistryShape(t *testing.T) {
	entries := readGapRegistry(t)
	if len(entries) == 0 {
		t.Fatalf("%s 没有任何数据行 —— 登记册空了必须显式删文件/留空说明，"+
			"否则「没有缺口」与「没人维护」看起来一模一样", gapRegistryPath)
	}
	seen := map[string]bool{}
	prevKey := ""
	for i, e := range entries {
		key := e.Table + "." + e.Column
		if seen[key] {
			t.Errorf("第 %d 行 %s 重复登记", i+1, key)
		}
		seen[key] = true
		if key < prevKey {
			t.Errorf("第 %d 行 %s 未按 (table, column) 升序（上一行 %s）", i+1, key, prevKey)
		}
		prevKey = key
		if e.Reason == "" {
			t.Errorf("第 %d 行 %s 的 reason 为空 —— 每条登记都必须写清「谁提供这列、为什么可以缺」", i+1, key)
		}
		if !gapSeverityAllowed[e.Severity] {
			t.Errorf("第 %d 行 %s 的 severity = %q，不在白名单 %v 里", i+1, key, e.Severity, gapSeverityAllowed)
		}
	}
}

// ── 门 A + 门 B：真库腿 ────────────────────────────────────────────────────

func TestFreshInstallColumnGapsMatchTheInstallChain(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿（一次性库 = installer 路径的复刻）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	entries := readGapRegistry(t)
	registered := readGapManifest(t)
	physical, present := readDbShape(ctx, t, pool)
	claims := readUnregisteredClaims(t, registered)

	registeredColumns := map[string]bool{}
	for _, e := range entries {
		registeredColumns[e.Table+"."+e.Column] = true
	}

	shape := gapShape{
		registeredFiles:   registered,
		physicalTables:    physical,
		presentColumns:    present,
		registeredColumns: registeredColumns,
	}
	det := detectGaps(shape, claims, entries)

	// 门 A：未登记文件带来的新缺口（登记册没写）⇒ 红。
	if len(det.UnregisteredGaps) > 0 {
		t.Errorf("未登记的 startup 迁移文件带来 %d 个新装库缺的列，而登记册 %s 里没有登记：\n  %s\n"+
			"  ⇒ 要么把那个文件登记进 installer 的 StartupFiles（改可部署链，需属主拍板），\n"+
			"     要么在本登记册里加一行并写清理由（已修完的缺口请先删掉过期行）。",
			len(det.UnregisteredGaps), gapRegistryPath, strings.Join(det.UnregisteredGaps, "\n  "))
	}

	// 门 B：登记册条目已不再缺 ⇒ 过期 ⇒ 红（提示删行）。
	if len(det.StaleEntries) > 0 {
		t.Errorf("登记册 %s 里有 %d 行已经不再缺（缺口被修好了）：\n  %s\n"+
			"  ⇒ 请把已修复的行从登记册里删掉 —— 登记册只登记「此刻仍然缺」的列。",
			gapRegistryPath, len(det.StaleEntries), strings.Join(det.StaleEntries, "\n  "))
	}
}

// ── 量具有牙的自检（纯函数合成数据，无需数据库） ──────────────────────────

// TestFreshInstallColumnGapDetectorHasTeeth 证明检测函数**能红也能绿**。
//
// ★ 为什么必须写这道自检：主体门的判定函数如果写反了（恒 true / 恒 false），
// 主体门会恒绿或恒红 —— 两种错误在读数上**长得一样**（都是「ok」），
// 看不出来（AGENTS.md：0 命中的第一嫌疑是量具坏了）。
// ⇒ 用合成数据把四种结论各自钉死：检出、放过、忽略视图、忽略已登记文件。
func TestFreshInstallColumnGapDetectorHasTeeth(t *testing.T) {
	base := gapShape{
		registeredFiles: map[string]bool{"registered.sql": true},
		// ★ t_view **故意不在** physicalTables 里：它在库里存在，但它是视图
		//   （relkind=v）⇒ 门不判它。把它放进 physicalTables 会让「视图不判」
		//   这条用例变成「关系不存在」，测不到想测的那条腿。
		physicalTables: map[string]bool{"t_real": true},
		presentColumns: map[string]map[string]bool{
			"t_real": {"existing": true},
			"t_view": {"whatever": true},
		},
		registeredColumns: map[string]bool{"t_real.known_gap": true},
	}
	entries := []gapRegistryEntry{
		{Table: "t_real", Column: "known_gap", Severity: "high", Reason: "synthetic"},
	}

	cases := []struct {
		name    string
		claims  map[string][]gapColumnClaim
		entries []gapRegistryEntry
		wantGap bool
	}{
		{
			name: "未登记文件加了一个没登记的缺列 ⇒ 必须红",
			claims: map[string][]gapColumnClaim{
				"unregistered.sql": {{Table: "t_real", Column: "brand_new"}},
			},
			entries: entries,
			wantGap: true,
		},
		{
			name: "未登记文件加的缺列已在登记册里 ⇒ 绿",
			claims: map[string][]gapColumnClaim{
				"unregistered.sql": {{Table: "t_real", Column: "known_gap"}},
			},
			entries: entries,
			wantGap: false,
		},
		{
			name: "已登记的文件不参与判定 ⇒ 绿",
			claims: map[string][]gapColumnClaim{
				"registered.sql": {{Table: "t_real", Column: "brand_new"}},
			},
			entries: entries,
			wantGap: false,
		},
		{
			name: "加在视图上的列不判 ⇒ 绿",
			claims: map[string][]gapColumnClaim{
				"unregistered.sql": {{Table: "t_view", Column: "brand_new"}},
			},
			entries: entries,
			wantGap: false,
		},
		{
			name: "列其实已存在 ⇒ 绿",
			claims: map[string][]gapColumnClaim{
				"unregistered.sql": {{Table: "t_real", Column: "existing"}},
			},
			entries: entries,
			wantGap: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			det := detectGaps(base, tc.claims, tc.entries)
			if tc.wantGap && len(det.UnregisteredGaps) == 0 {
				t.Fatalf("期望检出新缺口，实际没检出 —— 检测函数可能恒 false")
			}
			if !tc.wantGap && len(det.UnregisteredGaps) != 0 {
				t.Fatalf("期望不检出，实际检出 %v —— 检测函数可能恒 true", det.UnregisteredGaps)
			}
		})
	}

	// 门 B 的自检：登记册条目已不再缺 ⇒ 必须被标成过期。
	freshShape := base
	freshShape.presentColumns = map[string]map[string]bool{
		"t_real": {"existing": true, "known_gap": true},
		"t_view": {"whatever": true},
	}
	det := detectGaps(freshShape, nil, entries)
	if len(det.StaleEntries) != 1 || det.StaleEntries[0] != "t_real.known_gap" {
		t.Fatalf("期望标出过期条目 t_real.known_gap，实际 %v —— 过期检测可能恒 false", det.StaleEntries)
	}
}
