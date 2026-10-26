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
// ★★★ 订正（2026-10-21 R61 轮）：R60 的「23 列缺族」是**量具口径**的产物
//
//	R60 量的是「**装完、ensure 链没跑**」的库 ⇒ 23 条「生产有、新装没有」。
//	⇒ 但产品启动时的 ensure 链（db.Open）会自己 ADD COLUMN 补掉其中 **20 条**
//	（outbound_body / credentials 三列 / models_canonical 三列 / provider_catalog 三列
//	 / session_turns 的 host,tags 及其分区副本）⇒ 那些不是缺口。
//	⇒ 同口径复算（装完 + 启动一次）：新装 9245 列 / 生产 14122 列
//	⇒ 生产有、新装没有、且关系在两边都存在 ⇒ **26 条**，且**产品读写真正依赖的 = 0 条**
//	⇒ ⇒ 「全新安装连库都开不了」已不存在（R60 唯一实测致命那条已由 D1 修掉）。
//
//	⇒ ★ **本门因此加了一道「量具自检」**：oracle 必须是「装完 + 启动过」的形态
//	（判据 = 库里有没有 `models_canonical.standard_iq` —— **只有 ensure 链会加它**）。
//	没有 ⇒ 门**跳过并给出显式理由**，而不是给一个「装完但没启动」的失真读数
//	（AGENTS.md：0 命中的第一嫌疑是量具坏了；而「量具量错了东西」比「量具坏了」更隐蔽）。
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
//   - 登记册是「生产 vs 新装（装完+启动后）」的**纯差集**，
//     **不是**「产品实际引用了哪些列」的穷举 ⇒ 差集里没有列 ≠ 产品不缺列；
//     差集里有列 ≠ 产品缺列（每行 reason 里写了依赖判定）。
//   - ★ oracle 必须是「装完 + 启动过」的形态（见上文的量具自检），
//     否则读数是「装完但没启动」的失真值。

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// gapUpdate = true ⇒ 用真库读数**重生成**登记册（`-update`）。
// 判据纪律：登记册必须能被**重算**（否则它就是一份没人维护的手抄件）。
var gapUpdate = flag.Bool("update", false, "regenerate "+gapRegistryPath+" from the live DB")

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
	// FromCreate = true ⇒ 这条声明来自 `CREATE TABLE ... (...)` 形状，
	// false ⇒ 来自 `ALTER TABLE ... ADD COLUMN` 形状。
	//
	// ★ 为什么要分开（R61 轮实测）：
	//   - ALTER 形状：文件**跑过**就一定会把列加上 ⇒ 只有**未登记**（= 不会跑）
	//     的文件的 ALTER 声明才算缺口。
	//   - CREATE 形状：`CREATE TABLE IF NOT EXISTS` 在「表已存在」时是**静默空操作**
	//     ⇒ 哪怕文件**登记了、也跑了**，它声明的列照样加不上
	//     （startup/315 与基线 01-schema.sql:11398 的 prompt_injection_detections
	//     就是这个形态）⇒ 这类声明**不看登记与否**，一律算声明。
	FromCreate bool
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
		for _, c := range claims {
			if shape.registeredFiles[file] && !c.FromCreate {
				continue // 已登记 + ALTER 形状 ⇒ 会跑 ⇒ 列一定在
			}
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

// gapConstraintKeywords 是 CREATE TABLE 列表项里**不是列名**的约束开头词。
var gapConstraintKeywords = map[string]bool{
	"primary": true, "unique": true, "check": true, "foreign": true,
	"constraint": true, "exclude": true, "like": true, "partition": true,
	"inherits": true, "with": true, "period": true, "index": true,
}

// gapStripSQLComments 把 SQL 注释（-- 行注释 / /* */ 块注释）替换成空格（保留换行）。
//
// ★ 为什么必须剥（2026-10-21 R61 轮实测）：startup/120_session_audit.sql 的
// CREATE TABLE 列表项里有一行纯注释（-- pending / approved / rejected / timeout），
// 而它上方还有一句英文注释（-- so the admin can replay / inspect …）
// ⇒ 不剥注释时，解析器把**注释里的第一个词**当成了列名
// ⇒ 实测产出了幽灵列 approval_queue.so（库里根本没有这一列）
// ⇒ 幽灵列会同时污染登记册（多一行）与门的判定（要么报假缺口，要么被登记册吃掉）。
func gapStripSQLComments(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inSingle, inDouble, inLine, inBlock := false, false, false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
			continue
		case inBlock:
			if c == '*' && i+1 < len(text) && text[i+1] == '/' {
				inBlock = false
				b.WriteString("  ")
				i++
				continue
			}
			if c == '\n' {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
			continue
		case inSingle:
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				inSingle = false
			}
			continue
		case inDouble:
			b.WriteByte(c)
			if c == '"' {
				if i+1 < len(text) && text[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				inDouble = false
			}
			continue
		}
		switch {
		case c == '-' && i+1 < len(text) && text[i+1] == '-':
			inLine = true
			b.WriteString("  ")
			i++
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			inBlock = true
			b.WriteString("  ")
			i++
		case c == '\'':
			inSingle = true
			b.WriteByte(c)
		case c == '"':
			inDouble = true
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// createTableRe 抓 CREATE TABLE 语句的开头（表名 + 左括号位置）。
//
// ★ 为什么必须加这个解析面（2026-10-21 R61 轮实测）：
// startup/315 用的是 `CREATE TABLE IF NOT EXISTS prompt_injection_detections (...)`，
// 而**基线 01-schema.sql 已经建过这张表** ⇒ 那整段 CREATE 在全新安装里是**静默空操作**
// ⇒ 它声明的 4 个列（detection_score / matched_rules / matched_rules_count /
// detection_layers）在新装库里永远不会被加上去。
// ⇒ 而门 A 原来只解析 `ALTER TABLE ... ADD COLUMN` ⇒ **这类形状它一条都看不见**
// ⇒ 实测证据：把登记册里那 4 行删掉一门 A 依然绿（该形状完全没有被保护）。
// ⇒ 现在把 CREATE TABLE 的列也算成声明；「表在库里已存在」时（即 CREATE 是空操作）
// 它的列就是「声明了但缺」⇒ 必须登记。
var createTableRe = regexp.MustCompile(
	`(?is)CREATE\s+(?:TEMP\s+|TEMPORARY\s+|UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*\(`)

// parseCreateTableClaims 抽出 SQL 里所有 `CREATE TABLE ... ( ... )` 声明的列。
//
// 解析要点（都是踩过的坑）：
//   - 列列表里有**嵌套括号**（numeric(5,2) / CHECK (... IN (...))）⇒ 必须按括号深度切，
//     不能用正则非贪婪匹配到第一个 `);`。
//   - 列表项里混着**表约束**（PRIMARY KEY / UNIQUE / CHECK / FOREIGN KEY ...）⇒
//     它们不是列名 ⇒ 按首词过滤。
//   - 标识符可能带双引号 ⇒ 读引号里的内容。
func parseCreateTableClaims(sqlText string) []gapColumnClaim {
	var out []gapColumnClaim
	for _, loc := range createTableRe.FindAllStringSubmatchIndex(sqlText, -1) {
		table := strings.ToLower(strings.Trim(sqlText[loc[2]:loc[3]], `"`))
		if i := strings.LastIndex(table, "."); i >= 0 {
			table = table[i+1:]
		}
		if gapTableKeywords[table] {
			continue
		}
		// ⚠ 左括号**不一定**紧跟在表名后面（正则里是 `\s*\(` ⇒ 中间可能有空白/换行）
		// ⇒ 不能用 loc[3]-1 取位置（踩过：那样永远取不到括号 ⇒ 一条声明都抽不出来，
		//   而门 A 照样绿 ⇒ 看起来像「没问题」，实际是**量具瞎了**）。
		openIdx := strings.IndexByte(sqlText[loc[3]:], '(')
		if openIdx < 0 {
			continue
		}
		openIdx += loc[3]
		body, ok := extractBalancedParens(sqlText, openIdx)
		if !ok {
			continue
		}
		for _, item := range splitTopLevelCommas(body) {
			col := firstIdentifier(item)
			if col == "" {
				continue
			}
			if gapTableKeywords[col] || gapConstraintKeywords[col] {
				continue
			}
			out = append(out, gapColumnClaim{Table: table, Column: col, FromCreate: true})
		}
	}
	return out
}

// extractBalancedParens 从 openIdx（'(' 的位置）取出配对括号里的正文。
// 处理单引号字符串（含 ” 转义）与双引号标识符，避免把字符串里的括号算进去。
func extractBalancedParens(s string, openIdx int) (string, bool) {
	if openIdx < 0 || openIdx >= len(s) || s[openIdx] != '(' {
		return "", false
	}
	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			// 跳过单引号字符串（含 '' 转义与反斜杠转义）
			for i++; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i++
						continue
					}
					break
				}
			}
		case '"':
			for i++; i < len(s); i++ {
				if s[i] == '"' {
					if i+1 < len(s) && s[i+1] == '"' {
						i++
						continue
					}
					break
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[openIdx+1 : i], true
			}
		}
	}
	return "", false
}

// splitTopLevelCommas 按**顶层**逗号切分（嵌套括号内的逗号不算）。
func splitTopLevelCommas(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			for i++; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i++
						continue
					}
					break
				}
			}
		case '"':
			for i++; i < len(s); i++ {
				if s[i] == '"' {
					if i+1 < len(s) && s[i+1] == '"' {
						i++
						continue
					}
					break
				}
			}
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// firstIdentifier 取一个列表项里的第一个标识符（小写），没有则返回空串。
func firstIdentifier(item string) string {
	item = strings.TrimSpace(item)
	if item == "" {
		return ""
	}
	if item[0] == '"' {
		if end := strings.Index(item[1:], `"`); end >= 0 {
			return strings.ToLower(item[1 : 1+end])
		}
		return ""
	}
	end := 0
	for end < len(item) && (item[end] == '_' ||
		(item[end] >= 'a' && item[end] <= 'z') ||
		(item[end] >= 'A' && item[end] <= 'Z') ||
		(item[end] >= '0' && item[end] <= '9')) {
		end++
	}
	return strings.ToLower(item[:end])
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

// readStartupClaims 扫 startup 目录，抽出所有「声称给某表加了某列」的声明。
//
// ★ 两种形状的取样范围**不同**（R61 轮实测的教训）：
//   - ALTER TABLE ... ADD COLUMN：**只看未登记的文件**。
//     登记过的文件真跑过 ⇒ 列一定在（若后来被别的迁移 DROP 掉，那是另一个语义，
//     不能算「缺列」）。
//   - CREATE TABLE ... (...)：**看所有文件**（登记与否）。
//     `CREATE TABLE IF NOT EXISTS` 在「表已存在」时是**静默空操作**
//     ⇒ 登记了、跑了也一样加不上那些列。
func readStartupClaims(t *testing.T, registered map[string]bool) map[string][]gapColumnClaim {
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
		b, err := os.ReadFile(filepath.Join(gapStartupDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := gapStripSQLComments(string(b))
		var claims []gapColumnClaim
		if !registered[name] {
			claims = append(claims, parseAddColumnClaims(text)...)
		}
		claims = append(claims, parseCreateTableClaims(text)...)
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

// ── 登记册重生成（-update）──────────────────────────────────────────────────

// gapSkipDirs 是「产品引用」检索时**不**参与统计的目录。
//
// ★ 为什么排除 sql/migrations 与 embeddata：那些是**声明方**本身
// （startup 通道的 SQL 就是缺列的来源）⇒ 拿它们当「产品引用」会把
// 每个缺口都算成 high，等于没有判定。
var gapSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"docs": true, ".agents": true, "backups": true, "migrations": true,
	"embeddata": true, "web-mobile": true, ".idea": true,
}

// gapRefExts 是参与「产品引用」检索的文件后缀。
var gapRefExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".vue": true, ".js": true,
	".sql": true, ".py": true, ".sh": true, ".yaml": true, ".yml": true,
}

// gapProductHits 是一次「某列名在产品代码里被引用」的检索结果。
type gapProductHits struct {
	Product []string // 非测试文件（产品路径）
	Tests   []string // _test.go 文件（测试路径）
}

// gapRepoIndex 是「产品代码」的一次性索引（路径 → 内容）。
//
// ★ 为什么必须一次建索引：缺口有 132 列 ⇒ 若每列都重扫一遍全仓
// （实测 >10 分钟）⇒ 这门就没人愿意跑。
// ⇒ 建一次索引（内存里），之后每列只在内存里找 ⇒ 秒级。
type gapRepoIndex struct {
	// ids = 标识符（小写）→ 出现过它的文件列表。
	//
	// ★ 为什么是「一次分词」而不是「每列扫一遍」：
	//   参与判定的文件约 1.1 万个；缺口 132 列 ⇒ 逐列正则扫 = 30 GB 级扫描，
	//   实测跑了几分钟没结束（而且它自己就在这道门里 ⇒ 没人愿意跑）。
	// ⇒ 改成**一遍**把每个文件分词，建「标识符 → 文件」索引 ⇒ 之后每列是 O(1) 查表。
	ids map[string][]string
}

// gapBuildRepoIndex 扫一遍仓库，把参与「产品引用」判定的文件读进内存。
func gapBuildRepoIndex(repoRoot string) gapRepoIndex {
	idx := gapRepoIndex{ids: map[string][]string{}}
	_ = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if gapSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !gapRefExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if info.Size() > 4<<20 {
			return nil // 超大文件（生成物/数据）不参与判定
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		isTest := strings.HasSuffix(rel, "_test.go")
		for _, id := range gapIdentifiers(b) {
			files := idx.ids[id]
			if len(files) > 0 && files[len(files)-1] == rel {
				continue // 同一文件里重复出现只记一次
			}
			idx.ids[id] = append(files, rel)
		}
		_ = isTest
		return nil
	})
	return idx
}

// gapIdentifierRe 抓一个标识符（词边界 ⇒ status 不会命中 status_code）。
var gapIdentifierRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// gapIdentifiers 把一段文本切成小写标识符集合。
func gapIdentifiers(b []byte) []string {
	matches := gapIdentifierRe.FindAll(b, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.ToLower(string(m)))
	}
	return out
}

// gapFindColumnHits 检索列名在产品代码里的出现位置。
//
// ★ 口径：按**词边界**匹配（避免 status 命中 status_code），排除迁移目录。
func gapFindColumnHits(idx gapRepoIndex, column string) gapProductHits {
	var hits gapProductHits
	for _, rel := range idx.ids[strings.ToLower(column)] {
		if strings.HasSuffix(rel, "_test.go") {
			hits.Tests = append(hits.Tests, rel)
		} else {
			hits.Product = append(hits.Product, rel)
		}
	}
	sort.Strings(hits.Product)
	sort.Strings(hits.Tests)
	return hits
}

// gapSeverityFor 按「产品引用」检索结果给一条缺口定级。
//
//	high   产品路径（非测试代码）里引用了它 ⇒ 走那条路径会 42703
//	medium 只在测试代码里引用，或命中过多（列名太通用 ⇒ 按名判定不可靠）
//	benign 全仓没有命中 ⇒ 当前产品读写都不依赖它（遗留列）
func gapSeverityFor(hits gapProductHits) (string, string) {
	switch {
	case len(hits.Product) == 0 && len(hits.Tests) == 0:
		return "benign", "全仓无按名命中 ⇒ 当前产品读写都不依赖它（遗留列）"
	case len(hits.Product) == 0:
		return "medium", fmt.Sprintf("只在测试代码里按名命中 %d 处（%s）⇒ 产品路径未见按名引用", len(hits.Tests), gapShortList(hits.Tests))
	case len(hits.Product) > 20:
		return "medium", fmt.Sprintf("产品代码里按名命中 %d 处（列名太通用 ⇒ 按名判定不可靠，如 %s）", len(hits.Product), gapShortList(hits.Product))
	default:
		return "high", fmt.Sprintf("产品代码里按名命中 %d 处（%s）⇒ 走那条路径会 42703", len(hits.Product), gapShortList(hits.Product))
	}
}

// gapShortList 把命中列表压成可读的短串。
func gapShortList(files []string) string {
	if len(files) == 0 {
		return "无"
	}
	const max = 3
	if len(files) <= max {
		return strings.Join(files, ", ")
	}
	return strings.Join(files[:max], ", ") + fmt.Sprintf(" …（共 %d 处）", len(files))
}

// gapOverride 是登记册里的人工覆盖行。
//
// ★ 为什么需要覆盖机制：severity 的机械判定是「按列名在产品代码里检索」，
// 它**认不出别名/派生列** —— 典型反例：
// `prompt_injection_detections.detection_score` 在产品代码里确实出现，
// 但那是**读路径的派生别名**（risk_level AS detection_score），不是物理列
// ⇒ 机械判定会把它误判成 high。
// ⇒ 覆盖行写成注释（`# OVERRIDE <table>.<column> <severity> <reason>`），
//
//	这样 `-update` 重生成时**不会**把人的判断冲掉。
type gapOverride struct {
	severity string
	reason   string
}

// gapReadOverrides 从登记册里读人工覆盖行。
func gapReadOverrides(path string) map[string]gapOverride {
	out := map[string]gapOverride{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# OVERRIDE ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "# OVERRIDE "))
		parts := strings.SplitN(rest, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		sp := strings.SplitN(parts[1], "\t", 2)
		if len(sp) != 2 {
			continue
		}
		out[key] = gapOverride{severity: strings.TrimSpace(sp[0]), reason: strings.TrimSpace(sp[1])}
	}
	return out
}

// gapWriteRegistry 用真库读数重生成登记册。
func gapWriteRegistry(t *testing.T, path string, rows []gapRegistryEntry, overrides map[string]gapOverride) {
	t.Helper()
	var b strings.Builder
	b.WriteString(gapRegistryHeader)
	// 覆盖行必须**写回去**（否则重生成会把人的判断冲掉）。
	ovKeys := make([]string, 0, len(overrides))
	for k := range overrides {
		ovKeys = append(ovKeys, k)
	}
	sort.Strings(ovKeys)
	for _, k := range ovKeys {
		b.WriteString(fmt.Sprintf("# OVERRIDE %s\t%s\t%s\n", k, overrides[k].severity, overrides[k].reason))
	}
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n", r.Table, r.Column, r.Severity, r.Reason))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("已重生成 %s（%d 行）", path, len(rows))
}

// gapRegistryHeader 是登记册的头（说明文字）。
const gapRegistryHeader = "# fresh_install_column_gaps.tsv — 全新安装缺列登记册（★ 装完 **且启动过** 之后的形态）\n#\n# WHAT THIS FILE IS\n#   一份「全新安装的库，**装完并启动一次**之后，缺哪些列」的显式登记册。每行一条：\n#\n#       <table>	<column>	<severity>	<one-line reason>\n#\n#   「全新安装」= installer 的真实路径 **+ 产品的第一次启动**：\n#       embeddata/00-prereqs → embeddata/01-schema（基线）→ embeddata/02-seed\n#       → StartupFiles 登记的 startup 迁移（登记册 = installed_startup_migrations.tsv）\n#       → **db.Open 的 ensure 链**（产品启动时必跑）\n#\n#   ★ **「装完」不等于「装完能用」**：产品启动时的 ensure 链会自己补一批列\n#     （ADD COLUMN IF NOT EXISTS），而 installer's 迁移链**不会**\n#     ⇒ 两侧必须比「装完 **且启动过**」的形态，否则会把 ensure 自愈的列算成缺口。\n#\n#   本文件是**棘轮**：sql/schema/fresh_install_column_gaps_test.go 会\n#   (a) 要求每条登记在**此刻仍然缺**（缺了才登记；已修复 ⇒ 门红 ⇒ 请删掉该行），\n#   (b) 要求「startup 通道声明过、但库里没有」的每一列都出现在本清单里\n#       （漏登 ⇒ 门红 ⇒ 新缺口当场被抓住）。\n#   ⇒ 修好一个缺口 = 删一行；新缺口冒出来 = 补一行并写清理由。\n#\n# ★★★ 口径演进（2026-10-21，R60 → R61）\n#\n#   R60 的口径是「装完、ensure 链没跑」的库 ⇒ 量到 23 条 ⇒ 结论写进 handoff §十八。\n#   R61 轮复核（自己上一节的结论也要复核）：\n#     ① 换口径（装完 + 启动一次）⇒ 那 23 条里 20 条是 **ensure 自愈**的伪缺口\n#        （outbound_body / credentials 三列 / models_canonical 三列 / provider_catalog 三列\n#         / session_turns.host,tags 及其分区副本）⇒ 真缺口只剩 D1 修掉的那条。\n#     ② 但门 A 的**解析面**当时只认「ALTER TABLE ... ADD COLUMN」\n#        ⇒ 「CREATE TABLE IF NOT EXISTS」形状（表已存在 ⇒ **整段静默空操作**）\n#           一条都看不见 ⇒ 缺口被漏掉。\n#        ⇒ R61 把 CREATE 形状也纳入声明面；★ 且解析器必须**剥掉 SQL 注释**\n#          （否则注释里的第一个词会被当成列名 ⇒ 实测多出 100+ 条幽灵列）。\n#\n#   ⇒ **本清单现在的轴** = 「startup 通道声明过、但全新安装（装完+启动后）库里没有的列」。\n#     选这条轴是因为它**本地可算**（声明面 vs 库实况）⇒ 门能真的盯住它；\n#     「生产侧」是**人工核对**（无本地 oracle）⇒ 每行的生产判定随 reason 记录在案。\n#     它**不是**「生产 vs 新装」的差集（那条差集实测 26 列，**全是生产多留的历史列**，\n#     产品读写都不依赖 ⇒ 不在本清单里，见 handoff §18.13）。\n#\n#   ⇒ ★★ **severity 的第一道闸门 = 「生产侧有没有这一列」**（R62 轮实测确立）：\n#       ★ **按名命中对『不存在的列』必然是假阳性** —— 一个列在**生产里都不存在**，\n#         产品就不可能真的依赖它 ⇒ 机械按名命中的 high/medium 一律不成立。\n#       生产也没有（死 DDL） ⇒ severity 恒为 benign，且**不得据此补 ALTER**；\n#       生产有 ⇒ 才按按名命中数定 high / medium。\n#     ⇒ 机械判定（按列名在产品代码里检索）只作**参考**：\n#       high   产品路径（非测试代码）按名引用了它 ⇒ 走那条路径会 42703\n#       medium 只在测试代码里引用，或列名太通用（命中 >20 处）⇒ 按名判定不可靠\n#       benign 全仓无按名命中 ⇒ 当前产品读写都不依赖它（遗留列）\n#     ⚠ 按名检索**不能排除** SELECT * 形态的隐式依赖 ⇒ medium/high 都要人工复核。\n#\n#   ⇒ ★ **人工覆盖**：机械判定认不出「别名/派生列」（典型：`detection_score`\n#     在产品代码里是读路径的派生别名 `risk_level AS detection_score`，不是物理列）\n#     ⇒ 人工判断写成覆盖行（**注释行**，所以数据行解析会跳过它）：\n#         # OVERRIDE <table>.<column>	<severity>	<reason>\n#     `-update` 重生成时**保留**覆盖行 ⇒ 登记册既可重算，又不丢人的判断。\n#\n# ★★★ 口径演进（2026-10-21，R62 轮）：生产侧核对 + 三类假阳性 + 真缺口集 = ∅\n#\n#   R62 轮拿**生产 252** 把本清单的 20 列逐条核对 ⇒ 结论：\n#     - **16 列生产也没有** ⇒ 「没人跑过的死 DDL」（对应迁移的 CREATE 形状在\n#       『表已存在』上整段静默空操作 ⇒ 那段 DDL 从未真正生效过）\n#       ⇒ ★ 按名命中对这 16 列**必然是假阳性** ⇒ 其中 11 行原本被判 high/medium\n#         （diagnostic_runs.audit_log_id / goal_run_actions.tenant_id /\n#          goal_run_steps.tenant_id / goal_runs.fencing_token / goal_runs.goal_run_id /\n#          request_stage_events.trace_id / usage_ledger.{cost_currency,department,\n#          employee,id,position}）⇒ **全部降级为 benign** ⇒ 「补 20 个 ALTER」会补出\n#         11 个**没人依赖、连生产都没有**的列 ⇒ **该采纳项不执行**（见 handoff §19.8）。\n#     - **4 列生产有**（prompt_injection_detections.{detection_layers,detection_score,\n#       matched_rules,matched_rules_count}）⇒ 不是死 DDL，是真实的生产/新装形态差异；\n#       但产品读写**不依赖这些物理列**（读路径是派生别名 `risk_level AS detection_score`\n#       等）⇒ 仍是 benign ⇒ **不补**。\n#     ⇒ ★★ **真缺口集 = ∅**（20 列：16 死 DDL + 4 产品不依赖 ⇒ 没有一列需要补 ALTER）。\n#\n#   ⇒ ★ 三类假阳性（每一类都是「量具口径」问题，不是产品缺陷）：\n#     ① R60：量在「装完但 ensure 链没跑」⇒ 把 20 个 **ensure 自愈**的列算成缺口；\n#     ② R62：只按「startup 声明 vs 库里实况」判 ⇒ 把**生产也没有的死 DDL**算成缺口；\n#     ③ 若只按「生产 vs 新装」判 ⇒ 把**生产多留的历史列**算成缺口。\n#     ⇒ ★ **判缺口必须三侧对照**：生产有 ∧ 新装没有 ∧ 产品依赖。\n#\n# HOW TO ADD AN ENTRY\n#   跑：go test ./sql/schema/ -run TestFreshInstallColumnGapsMatchTheInstallChain -update\n#   （它用真库读数重生成整份清单 ⇒ 登记册永远可重算，不是一份手抄件）\n#   ⚠ 该命令会**写文件** ⇒ 只在一次性库上跑。\n#\n# ⚠ 已知边界（写在前面，别让后人误读这份清单的覆盖面）：\n#   - 本清单是「startup 通道的声明 vs 全新安装的实况」的差集，\n#     **不是**「产品实际用到的列」的穷举。\n#   - domain 通道与 root 通道的文件不在 startup 目录里 ⇒ 门检不出来。\n#   - 门只认**物理表**（relkind ∈ r/p）⇒ 视图上的列不在检测范围内。\n#   - ★ 门要求 oracle 是「装完 + 启动过」的形态：库里若缺\n#     models_canonical.standard_iq（只有 ensure 链会加它）⇒ 门**跳过并给出显式理由**。\n"

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

	// ── 量具自检：oracle 必须是「装完 **且启动过**」的形态 ──────────────────
	// `models_canonical.standard_iq` **只有 ensure 链会加它**
	//（db/db.go:5654 的 ensureModelsCanonicalStandardIQ；installer 的迁移链
	// 不加它 —— 350 在 domain 通道，installer 不跑）⇒ 它在 = 这个库跑过 db.Open。
	// 没有它 ⇒ 本门量的是「装完但**没启动**」⇒ 读数失真
	//（R60 轮的「23 列缺族」正是这么来的：20 条其实是 ensure 自愈的列）。
	// ⇒ 这里**跳过并说清理由**，而不是给一个「看着像绿灯」的失真读数
	//（AGENTS.md：0 命中的第一嫌疑是量具坏了；而「量具量错了对象」更隐蔽）。
	var marker int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'models_canonical'
		  AND column_name = 'standard_iq'`).Scan(&marker); err != nil {
		t.Fatalf("read oracle marker (models_canonical.standard_iq): %v", err)
	}
	if marker == 0 {
		t.Skip("oracle 不是「装完 + 启动过」的形态（库里没有 models_canonical.standard_iq，" +
			"而它只有 ensure 链会加）⇒ 本门量的是「装完但没启动」的失真形态，跳过。" +
			"请用 scripts/audit/probe-onetime-db-shape.sh 建库（它现在会跑 ensure 链）。")
	}

	entries := readGapRegistry(t)
	registered := readGapManifest(t)
	physical, present := readDbShape(ctx, t, pool)
	claims := readStartupClaims(t, registered)

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

	// ── -update：用真库读数**重生成**登记册 ────────────────────────────────
	//
	// ★ 为什么登记册必须可重算：一份手抄的清单会在改口径后悄悄过期
	//（R60 → R61 就是这么发生的：口径从「装完」换成「装完+启动」，
	//  行数从 17 变 132，而清单本身不会叫醒任何人）。
	if *gapUpdate {
		repoRoot := "../.."
		repoIdx := gapBuildRepoIndex(repoRoot)
		overrides := gapReadOverrides(gapRegistryPath)
		// 文件名排序 ⇒ 同一列被多个文件声明时，取文件名最小的那个作为来源。
		files := make([]string, 0, len(claims))
		for f := range claims {
			files = append(files, f)
		}
		sort.Strings(files)
		type gapRow struct {
			table, column, file string
			fromCreate          bool
		}
		byKey := map[string]gapRow{}
		for _, file := range files {
			for _, c := range claims[file] {
				if !physical[c.Table] {
					continue // 关系不存在或不是物理表（视图）⇒ 不判
				}
				if cols := present[c.Table]; cols != nil && cols[c.Column] {
					continue // 列已存在 ⇒ 不是缺口
				}
				key := c.Table + "." + c.Column
				if _, ok := byKey[key]; ok {
					continue
				}
				byKey[key] = gapRow{table: c.Table, column: c.Column, file: file, fromCreate: c.FromCreate}
			}
		}
		keys := make([]string, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rows := make([]gapRegistryEntry, 0, len(keys))
		for _, k := range keys {
			r := byKey[k]
			hits := gapFindColumnHits(repoIdx, r.column)
			sev, reason := gapSeverityFor(hits)
			shapeWord := "ALTER TABLE ... ADD COLUMN"
			if r.fromCreate {
				shapeWord = "CREATE TABLE ... (表已存在 ⇒ 整段是静默空操作)"
			}
			if ov, ok := overrides[r.table+"."+r.column]; ok {
				sev = ov.severity
				reason = ov.reason
			}
			rows = append(rows, gapRegistryEntry{
				Table:    r.table,
				Column:   r.column,
				Severity: sev,
				Reason:   fmt.Sprintf("%s 以 %s 形状声明；%s", r.file, shapeWord, reason),
			})
		}
		gapWriteRegistry(t, gapRegistryPath, rows, overrides)
		return
	}

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
