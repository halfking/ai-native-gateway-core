package dbinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════
// 本文件是 TestNoTransactionMarkerIsJustified 的【逆向】不变式。
//
// 既有守卫（no_transaction_test.go）说的是：
//
//	「带 dbinit:no-transaction 标记 ⇒ 必须真的含不可事务化语句」
//	（防豁免扩散）
//
// 本文件说的是它的**逆向**：
//
//	「真含不可事务化语句 ⇒ 必须带 dbinit:no-transaction 标记」
//	（防**丢标记**）
//
// ★ ★ 两条合起来 ⇒ 标记与内容【双向绑定】⇒ 任何一侧单独漂移都会被抓住。
//
// ★ ★ 为什么补这条（2026-10-23 §143.97 会话族 hot 排水轮）：
//
//	§143.97 的变异台有一臂 M04「去掉 dbinit:no-transaction 标记」，
//	首跑读数是「期望红但门仍绿 ⇒ 门没牙」。
//	★ 而逐字复核后的真实结论是：**那一臂是惰性变异，不是门没牙** ——
//	★ 898 里标记有两种拼写（首行 `-- dbinit:no-transaction` 与
//	★ 第 139 行 `-- ★ ★ dbinit:no-transaction 标记`），
//	★ 而 installer's requiresNoTransaction 判的是【谓词】
//	★ （存在一行 `--` 注释且含该 token）⇒ ★ 删掉一种拼写后
//	★ 谓词仍为 true ⇒ ★ 非事务通道仍在 ⇒ ★ 变异没有造成功能回归。
//
//	★ 但这暴露了一个**真实的缺口**：仓库里没有任何门断言
//	★ 「**被 installer 真正应用的那些文件**在需要非事务通道时必须带标记」
//	★ ⇒ ⇒ 一个人把 898 的**两种拼写都**删掉 ⇒ 全新安装会死在
//	★ 「CREATE INDEX CONCURRENTLY cannot run inside a transaction block」
//	★ ⇒ ⇒ 而没有任何门会红。
//
//	★ 本文件就是补这个缺口的：它**调用真函数** requiresNoTransaction
//	★ （不是拷贝一份判据）⇒ ⇒ installer 改了标记口径或 token 时，
//	★ 本门自动跟着变 ⇒ ⇒ 不会出现「门守的是一份拷贝」。
// ═══════════════════════════════════════════════════════════════════════

// untransactableTokens 是 PostgreSQL 明确不能在事务块内执行的语句形态。
//
// ★ 与 no_transaction_test.go 的 untransactable 同款，但这里用的是
// ★ 【词边界】口径 —— ★ 裸子串会把 autovacuum_vacuum_scale_factor
// ★ 这类 reloption 名误判成 VACUUM 语句（★ 2026-10-23 实测：
// ★ embeddata 全目录裸子串口径有 3 个假阳性，词边界口径 0 个）。
var untransactableTokens = []string{
	"DROP INDEX CONCURRENTLY",
	"CREATE INDEX CONCURRENTLY",
	"REINDEX CONCURRENTLY",
	"CREATE DATABASE",
	"VACUUM",
}

// repoRootFromDBInit 定位仓库根（installer/internal/dbinit → 仓库根）。
func repoRootFromDBInit(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..", "..")
}

// stripSQLComments14397 剥掉 SQL 注释，得到【可执行面】。
//
// ★ 剥两种注释：
//   - /* … */ 块注释（跨行）
//   - -- 行注释
//
// ★ 行注释的剥离**跳过单引号字符串字面量** —— ★ 否则一个字符串里的
// ★ `--` 会把该行后半截（含真的语句）误删 ⇒ ⇒ 造成漏判。
//
// ★ ★ 刻意【不剥字符串字面量的内容】—— ★ 因为 898 用
// ★ `SELECT format('CREATE INDEX CONCURRENTLY IF NOT EXISTS %I …') \gexec`
// ★ 投递 ⇒ ★ DDL 就住在字符串字面量里 ⇒ ⇒ 若把字面量内容也剥掉，
// ★ 898 这类文件在判据眼里会**完全消失** ⇒ ⇒ ⇒ 门就漏掉了它的主战场。
//
// ★ 刻意**不**处理 dollar-quoted（$$…$$ / $function$…$function$）：
// ★ 实测（embeddata 全 302 个 StartupFiles）词边界口径 0 违反，
// ★ 加上 dollar-quote 处理会引入正则状态机复杂度而不增加判别力。
func stripSQLComments14397(sql string) string {
	// 先剥块注释。
	sql = stripBlockComments14397(sql)

	var out []string
	for _, line := range strings.Split(sql, "\n") {
		out = append(out, cutLineComment14397(line))
	}
	return strings.Join(out, "\n")
}

// stripBlockComments14397 剥掉 /* … */ 块注释（支持跨行）。
func stripBlockComments14397(sql string) string {
	var b strings.Builder
	for {
		i := strings.Index(sql, "/*")
		if i < 0 {
			b.WriteString(sql)
			return b.String()
		}
		b.WriteString(sql[:i])
		rest := sql[i+2:]
		j := strings.Index(rest, "*/")
		if j < 0 {
			// 未闭合：后面全部当注释丢掉（保守：不会误留注释内容）。
			return b.String()
		}
		sql = rest[j+2:]
	}
}

// cutLineComment14397 砍掉一行里【字符串字面量之外】的 -- 注释。
func cutLineComment14397(line string) string {
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inQuote {
			if c == '\'' {
				// '' 是转义的单引号，不出串。
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				inQuote = false
			}
			continue
		}
		if c == '\'' {
			inQuote = true
			continue
		}
		if c == '-' && i+1 < len(line) && line[i+1] == '-' {
			return line[:i]
		}
	}
	return line
}

// containsToken14397 是【词边界】子串匹配：tok 前后都不能是
// [A-Z0-9_]（★ 大写化后比较 ⇒ 标识符字符即 [A-Z0-9_]）。
//
// ★ 与 cmd/gateway/section_14395_session_turns_retention_test.go 的
// ★ containsToken14395 同款语义（★ 那里 `_` 与 `-` 都不算边界）。
func containsToken14397(haystack, tok string) bool {
	for i := 0; i+len(tok) <= len(haystack); i++ {
		if haystack[i:i+len(tok)] != tok {
			continue
		}
		if i > 0 && isIdentByte14397(haystack[i-1]) {
			continue
		}
		if i+len(tok) < len(haystack) && isIdentByte14397(haystack[i+len(tok)]) {
			continue
		}
		return true
	}
	return false
}

func isIdentByte14397(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// untransactableIn14397 报告可执行面里出现的不可事务化语句。
func untransactableIn14397(executable string) []string {
	upper := strings.ToUpper(executable)
	var hits []string
	for _, tok := range untransactableTokens {
		if containsToken14397(upper, tok) {
			hits = append(hits, tok)
		}
	}
	return hits
}

// TestStartupFilesWithUntransactableStatementsAreMarked 是逆向不变式。
//
// ★ 判据范围 = **installer 真正会应用的文件集**（dbinit.NewRunner 的
// ★ StartupFiles）⇒ ⇒ 不是「目录里有什么」而是「通道会跑什么」。
// ★ 字节来源 = **embeddata 副本**（★ setupSQLDir 把 embedded 字节写到
// ★ 临时目录 ⇒ applySQL 从那儿读 ⇒ ⇒ 那才是真正喂给 psql 的内容）。
//
// ★ 同时要求 **canonical 副本**（仓库 sql/migrations/startup/）也带标记：
// ★ 两份必须逐字节一致（六点位登记已守）⇒ ⇒ 任一份丢标记都会被抓住。
func TestStartupFilesWithUntransactableStatementsAreMarked(t *testing.T) {
	runner := NewRunner("", "", "", "")
	if len(runner.StartupFiles) == 0 {
		t.Fatalf("StartupFiles 为空 —— 判据失效：★ 若登记机制已改名，请连同本门一起改")
	}

	embedStart := filepath.Join(embeddataRoot(t), "startup")
	repoStart := filepath.Join(repoRootFromDBInit(t), "sql", "migrations", "startup")

	checked := 0
	for _, name := range runner.StartupFiles {
		b, err := os.ReadFile(filepath.Join(embedStart, name))
		if err != nil {
			t.Errorf("★ 读取 embeddata/startup/%s 失败：%v", name, err)
			continue
		}
		hits := untransactableIn14397(stripSQLComments14397(string(b)))
		if len(hits) == 0 {
			continue
		}
		checked++

		// ★ 判据用**真函数**（★ 不是拷贝）⇒ installer 改口径时本门自动跟随。
		if !requiresNoTransaction(b) {
			t.Errorf("★ %s 的可执行面含不可事务化语句 %v，"+
				"但**没有** %s 标记 ⇒ ⇒ installer 的 applySQL 会给它加 "+
				"--single-transaction ⇒ ⇒ 全新安装会死在 "+
				"「CREATE INDEX CONCURRENTLY cannot run inside a transaction block」",
				name, hits, noTransactionMarker)
		}

		// ★ canonical 副本也要带（★ 两份必须一致 ⇒ 任一份漂移都被抓住）。
		cb, err := os.ReadFile(filepath.Join(repoStart, name))
		if err != nil {
			continue // ★ canonical 不存在（只有 embeddata 副本）⇒ 跳过。
		}
		if !requiresNoTransaction(cb) {
			t.Errorf("★ canonical sql/migrations/startup/%s 的可执行面含不可事务化语句 %v，"+
				"但**没有** %s 标记 ⇒ ⇒ 两份副本已漂移",
				name, hits, noTransactionMarker)
		}
	}

	// ★ 自证有牙：★ 至少要有若干个文件命中「含不可事务化语句」，
	// ★ 否则这条门可能是「扫了个寂寞」的恒绿门。
	if checked == 0 {
		t.Fatalf("★ StartupFiles 里没有任何文件含不可事务化语句 —— " +
			"★ 判据失效：本门此刻在扫空集（★ 若这些语句真的都不在了，" +
			"★ 请连同本门一起删，而不是留着失效的机制）")
	}
	t.Logf("StartupFiles=%d，其中含不可事务化语句并被核对标记的文件=%d",
		len(runner.StartupFiles), checked)
}

// TestStripSQLComments14397SelfCheck 是【扫描器自证有牙】：
// 证明可执行面提取器与词边界匹配器确实能分辨该分辨的东西。
//
// ★ 没有这条，一个「什么都合不出来」的提取器会让上一条门恒绿。
func TestStripSQLComments14397SelfCheck(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "行注释里的 VACUUM 不算",
			in:   "-- 这里提到 VACUUM 但不是语句\nSELECT 1;",
			want: nil,
		},
		{
			name: "块注释里的 CREATE INDEX CONCURRENTLY 不算",
			in:   "/* CREATE INDEX CONCURRENTLY x */\nSELECT 1;",
			want: nil,
		},
		{
			name: "裸子串 VACUUM 不算（autovacuum reloption）",
			in:   "WITH (autovacuum_vacuum_scale_factor='0.05');",
			want: nil,
		},
		{
			// ★ ★ 关键用例：★ 字符串字面量里的 `--` **不该**截断该行后半截
			// ★ （否则会漏掉真的语句）⇒ ⇒ 且字面量里的 DDL 关键词
			// ★ **必须保留** —— ★ 因为 898 用 `SELECT format('CREATE INDEX
			// ★ CONCURRENTLY …') \gexec` 投递 ⇒ ★ DDL 就住在字符串里
			// ★ ⇒ ⇒ 若把字面量内容剥掉，898 这类文件就**完全看不见**了
			// ★ ⇒ ⇒ 这是「剥注释、但不剥字面量」的理由。
			name: "字符串字面量里的 -- 不截断后续语句；字面量里的 DDL 保留",
			in:   "INSERT INTO t VALUES ('-- CREATE INDEX CONCURRENTLY');\nVACUUM;",
			want: []string{"CREATE INDEX CONCURRENTLY", "VACUUM"},
		},
		{
			name: "真的 VACUUM 语句要抓到",
			in:   "VACUUM ANALYZE public.t;",
			want: []string{"VACUUM"},
		},
		{
			name: "真的 CREATE INDEX CONCURRENTLY 要抓到",
			in:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS public.idx ON public.t(ts);",
			want: []string{"CREATE INDEX CONCURRENTLY"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := untransactableIn14397(stripSQLComments14397(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("★ untransactableIn14397(%q) = %v，want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("★ untransactableIn14397(%q) = %v，want %v", c.in, got, c.want)
				}
			}
		})
	}
}
