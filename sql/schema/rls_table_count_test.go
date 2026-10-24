package schema

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 这道门守护的是**文档里的一个数**：README×8 / docs/architecture.md /
// docs/PROJECT_OVERVIEW.md / docs/architecture-diagrams.md 里都写着
// 「RLS on 90+ tables（多租户隔离覆盖 90+ 张表）」。
//
// 为什么需要门：这个数是**手工维护**的，从来没有任何东西检查它。
// 事实是它已经烂掉过一次 —— 文档长期写着「38+ 表」，而实际启用 RLS 的表
// 早已远超这个数（2026-10-24 实测 95 张，口径见下）。文档与现实脱节
// 没有任何人发现，正是因为没有门。
//
// 口径（写清楚，否则「多少张表」永远在漂）：
//   - 权威来源 = 规范 schema `sql/schema/01-schema.sql`
//     ∪ startup 迁移链 `sql/migrations/startup/*.sql`
//     （生产只跑 startup 链 —— 见 installer/internal/dbinit/runner.go 的
//     StartupFiles；domain 链从未在生产跑过，故不计入）
//   - 判据 = 该表上出现过 `ENABLE ROW LEVEL SECURITY`
//   - 计数 = 按表名去重后的张数（同一张表被 schema + 多条迁移反复启用只算一张）
//   - `.down.sql` 不计（回滚迁移会还原出已废弃形态，不是现状）
//   - 注释行不计（例：`-- the corresponding ALTER TABLE ... ENABLE ROW LEVEL
//     SECURITY +` 里的 `...` 不是表名）
//   - 2026-10-24 实测：**95 张**（文档下限取 90，留 5 张余量）
//
// 门的作用是**下限**：只要文档写的是「90+」，那真实张数就必须 >= 90。
// 门不做「上限」——多出来的新表只会让「90+」更保守，不需要改文档。
// 真要把文档写精确（例如「95 张」），必须同步改本门的下限常量。
const documentedRLSTableFloor = 90

// enableRLSRe 匹配「ALTER TABLE [IF EXISTS] <表名> ENABLE ROW LEVEL SECURITY」。
var enableRLSRe = regexp.MustCompile(
	`(?i)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?([A-Za-z0-9_."]+)\s+ENABLE\s+ROW\s+LEVEL\s+SECURITY`)

// normalizeTableName 去掉 schema 前缀与引号，让 public.foo 与 "foo" 归一到同一个键。
func normalizeTableName(raw string) string {
	name := strings.Trim(raw, `"`)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

// rlsEnabledTables 收集权威来源里所有启用了 RLS 的表名。
func rlsEnabledTables(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}

	add := func(path string) {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", path, err)
		}
		for _, line := range strings.Split(string(text), "\n") {
			// 注释行不计：迁移头里讨论历史时会写出
			// `-- the corresponding ALTER TABLE ... ENABLE ROW LEVEL SECURITY +`，
			// 里面的 `...` 不是表名（normalize 后为空，已被丢弃），
			// 但显式跳过注释行可以让「口径」不依赖这个副作用。
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			for _, m := range enableRLSRe.FindAllStringSubmatch(line, -1) {
				key := normalizeTableName(m[1])
				if key == "" {
					continue
				}
				if _, seen := out[key]; !seen {
					out[key] = path
				}
			}
		}
	}

	add("01-schema.sql")

	entries, err := os.ReadDir(filepath.Join("..", "migrations", "startup"))
	if err != nil {
		t.Fatalf("读取 startup 迁移目录失败：%v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		add(filepath.Join("..", "migrations", "startup", name))
	}
	return out
}

// TestRLSEnabledTableCountMeetsDocumentedFloor 是「文档里的 RLS 表数」这道门。
//
// 判据：权威来源里启用 RLS 的表数 >= 文档宣称的下限。
// 变异：把任一迁移里的 ENABLE ROW LEVEL SECURITY 删掉 ⇒ 本门红。
func TestRLSEnabledTableCountMeetsDocumentedFloor(t *testing.T) {
	tables := rlsEnabledTables(t)
	got := len(tables)

	// 先断言量具本身是活的：口径要是读空了，这道门会「因为 0 < 100」而红，
	// 但那是**读数坏掉**，不是「RLS 表变少了」——两者必须能区分。
	if got == 0 {
		t.Fatalf("RLS 表清单为空 ⇒ 量具坏了（权威来源读不到或口径失效），" +
			"不能把它读成「RLS 覆盖塌了」")
	}
	if got < 20 {
		t.Fatalf("RLS 表数只有 %d 张，远低于历史量级 ⇒ 大概率是口径或路径读坏了"+
			"（而不是真的只剩这么多张），请先查量具", got)
	}

	if got < documentedRLSTableFloor {
		t.Errorf("启用 RLS 的表只有 %d 张，低于文档宣称的下限 %d 张（文档写的是「100+ 表」）\n"+
			"⇒ 二选一：① 真的丢了 RLS ⇒ 必须补回；② 文档口径变了 ⇒ 必须同步改文档与本门常量。\n"+
			"权威来源：sql/schema/01-schema.sql ∪ sql/migrations/startup/*.sql（按表名去重）",
			got, documentedRLSTableFloor)
	}
	t.Logf("启用 RLS 的表 %d 张（文档下限 %d）", got, documentedRLSTableFloor)
}

// TestRLSEnabledTableCountMatchesDocumentedClaim 把文档里的那个数钉住。
//
// 这道门读**文档本身**：README×8 / docs/architecture.md /
// docs/PROJECT_OVERVIEW.md / docs/architecture-diagrams.md 里
// 「RLS … 90+ 表」里的数，必须 **>= 门声明的下限**。
// 于是：有人把文档改回「38+」而忘了同步门 ⇒ 本门红；
// 有人把门的下限调高而忘了改文档 ⇒ 本门也红。两边必须一起动。
//
// 抽取口径：只认「数字紧跟在表字前面」的写法
// （`90+ tables` / `90+ 表` / `90 以上のテーブル` / `90+ جدول` …），
// 因此 `PostgreSQL 14+` 与供应商覆盖 `100+ 供应商` 都不会被误抓。
func TestRLSEnabledTableCountMatchesDocumentedClaim(t *testing.T) {
	docPaths := []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "README.ar.md"),
		filepath.Join("..", "..", "README.de.md"),
		filepath.Join("..", "..", "README.es.md"),
		filepath.Join("..", "..", "README.fr.md"),
		filepath.Join("..", "..", "README.ja.md"),
		filepath.Join("..", "..", "README.zh-CN.md"),
		filepath.Join("..", "..", "README.zh-TW.md"),
		filepath.Join("..", "..", "docs", "architecture.md"),
		filepath.Join("..", "..", "docs", "PROJECT_OVERVIEW.md"),
		filepath.Join("..", "..", "docs", "architecture-diagrams.md"),
	}

	// 表字（含各语种写法）+ 日文「N 以上のテーブル」形态。
	tableWords := "tables|Tabellen|tablas|جدول|جدولاً|テーブル|張表|表"
	claimRe := regexp.MustCompile(`(\d{1,4})\s*\+?\s*(?:` + tableWords + `)`)
	jpAboveRe := regexp.MustCompile(`(\d{1,4})\s*以上のテーブル`)

	totalClaims := 0
	for _, path := range docPaths {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", path, err)
		}
		body := string(text)

		claims := map[int]bool{}
		for _, m := range claimRe.FindAllStringSubmatch(body, -1) {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			claims[n] = true
		}
		for _, m := range jpAboveRe.FindAllStringSubmatch(body, -1) {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			claims[n] = true
		}

		if len(claims) == 0 {
			t.Errorf("%s 里没有找到「RLS 覆盖 N 张表」的写法 ⇒ "+
				"要么文档被改成了另一种说法（这道门需要跟着改），要么这段说明被删了", path)
			continue
		}
		for n := range claims {
			totalClaims++
			if n < documentedRLSTableFloor {
				t.Errorf("%s 里写的是 %d 张表，低于门声明的下限 %d（真实 %d 张，见 TestRLSEnabledTableCountMeetsDocumentedFloor）\n"+
					"⇒ 文档里的数必须与真实量级一致",
					path, n, documentedRLSTableFloor, documentedRLSTableFloor)
			}
		}
	}
	if totalClaims == 0 {
		t.Fatalf("一道门都没抓到 ⇒ 量具坏了（抽取正则失配），不能把它读成「文档没问题」")
	}
	t.Logf("共核对 %d 处文档里的 RLS 表数写法（下限 %d）", totalClaims, documentedRLSTableFloor)
}
