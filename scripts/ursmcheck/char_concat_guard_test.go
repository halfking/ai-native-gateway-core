package ursm_test

// char_concat_guard_test.go —— 「`"char"` 类型列参与 `||` 拼接」的全仓守卫。
//
// # 为什么要有这道门（2026-10-21，R93 轮实测）
//
// PG 里有一类列的类型是 **`"char"`**（单字符），不是 `char`、不是 `text`。
// 它们参与 `||` 拼接时，真库报：
//
//	ERROR:  operator is not unique: "char" || unknown
//	HINT:   Could not choose a best candidate operator. You might need to add explicit type casts.
//
// ★ 而**假 psql 桩对 SQL 照单全收，永远绿**（本仓 §82 已经为这个形态栽过一次：
// `scripts/ursmcheck/index_reader_census_test.go` 的 `TestCensusSQLIsRealNotJustStubs`
// 守的正是**一个**脚本里的 `c.relkind || ...`）。
//
// ★★ **那道门只守一个文件** ⇒ 仓里别处再写一次，它看不见。R93 轮用 PG 实测的
// `"char"` 列全集重扫全仓，抓到真阳性**分散在两个性质完全不同的目录**：
//
//	.db-audit/sql/04_slow2.sql:26                       （t2.attstorage / t2.attcompression）
//	scripts/local-dev/verify-multi-db-consistency.sh:51 （contype）
//
// ⇒ ⇒ 一次手修会漏下一次新增 ⇒ ⇒ **这才是该建门的理由**
// （不是「形态出现过」就建门 ⇒ 见「边界 ③」）。
//
// # 判据（★ 用分词，不用正则断言）
//
// 白名单 = **PG 实测**的 `pg_catalog` 里 `udt_name='char'` 的**全部**列名（45 个去重列名）。
//
// ★ 为什么必须是**全集**而不是手挑几个：
// R93 轮第一版只修了量到的 3 个列（relkind / attstorage / attcompression）
// ⇒ ⇒ 重测后**仍残留 1 条** `a.attalign` ⇒ ⇒ **「修样本」不等于「修族」**。
//
// 判「裸拼接」只认两种形态（其余不报，避免误报）：
//
//	<col> || …      作左操作数
//	… || <col>      作右操作数（允许中间隔着「别名.」）
//
// ★ **为什么不用正则**：Go 的 RE2 **既不支持 lookbehind 也不支持 lookahead**
// ⇒ ⇒ 而「`col` 后面不能跟着 `::cast`」这类否定断言正好需要 lookahead
// ⇒ ⇒ 所以判据改成**按标识符切 token**（纯字符串逻辑，无断言）：
//   · 左操作数 = token 是白名单列名，且**下一个 token 就是 `||`**
//     （`relkind::text ||` 的下一个 token 是 `::` ⇒ 不报 ✔）
//   · 右操作数 = token 是白名单列名，且**向前跳过「标识符 + `.`」后就是 `||`**
//     （`|| c.relkind` ⇒ 报 ✔；`|| relkind::text` ⇒ 下一个 token 是 `::` ⇒ 不报 ✔）
//
// # 边界（写明，免得将来有人以为它覆盖更多）
//
// ① **注释按文件扩展名分派**：SQL 是 `--` 与 `/* */`，Go 是 `//` 与 `/* */`，
//    shell/python 是 `#`。
//    ★★ 曾经用 `//` 当通用标记 ⇒ **SQL 行被误截** ⇒ 漏掉了一处真阳性
//    ⇒ ⇒ **漏放比误报危险** ⇒ ⇒ 注释语法必须分派。
// ② 字符串字面量里的内容不解析（不引号感知）⇒ 极小概率误报/漏报。
// ③ **只管「形态已经存在过、且分散在多处」的族**。`round(double precision, int)`
//    （PG 12+ 移除）与 `pg_stat_user_tables.reltuples`（PG 14+ 改名）**判据不可靠**
//    ⇒ ⇒ **不进这道门**（进了就是恒绿门/误报门）⇒ ⇒ 它们登记在
//    `scripts/audit/dbaudit-scripts-ledger.tsv` 里。
// ④ `scripts/ursmcheck/` 目录**整体豁免**（沿用仓内既有惯例：
//    `destructive_target_guard_test.go` 也跳过它）⇒ ⇒ 门文件里新写的 SQL 不在射程。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r93CharColumns 是 PG 17.10 实测的 `pg_catalog` 里 `udt_name='char'` 的列名全集（去重 45 个）。
//
// 来源（可复算）：
//
//	SELECT DISTINCT column_name FROM information_schema.columns
//	 WHERE table_schema='pg_catalog' AND udt_name='char' ORDER BY 1;
//
// ★ 原始清单是 46 行（`pg_depend.deptype` 与 `pg_shdepend.deptype` 同名）⇒ 去重 45。
var r93CharColumns = []string{
	"aggfinalmodify", "aggkind", "aggmfinalmodify", "amtype", "amoppurpose",
	"attalign", "attcompression", "attgenerated", "attidentity", "attstorage",
	"castcontext", "castmethod", "relkind", "relpersistence", "relreplident",
	"collprovider", "confdeltype", "confmatchtype", "confupdtype", "contype",
	"datlocprovider", "defaclobjtype", "deptype", "partmethod", "repmodel",
	"shardstorage", "evtenabled", "privtype", "oprkind", "partstrat", "polcmd",
	"prokind", "proparallel", "provolatile", "ev_enabled", "ev_type",
	"substream", "subtwophasestate", "srsubstate", "tgenabled", "typalign",
	"typcategory", "typdelim", "typstorage", "typtype",
}

// r93CharColumnSet 是白名单的集合形式。
var r93CharColumnSet = func() map[string]bool {
	m := make(map[string]bool, len(r93CharColumns))
	for _, c := range r93CharColumns {
		m[c] = true
	}
	return m
}()

// r93IsIdentRune 判定一个字符能否出现在标识符里。
func r93IsIdentRune(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// r93Tokenize 把一行代码切成 token：标识符、运算符（`||`、`::`、`.`）与其它单字符。
//
// ★ 分词而不是正则断言的理由见文件头「判据」一节（RE2 没有 lookahead）。
func r93Tokenize(line string) []string {
	var out []string
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case r93IsIdentRune(c):
			j := i
			for j < len(line) && r93IsIdentRune(line[j]) {
				j++
			}
			out = append(out, strings.ToLower(line[i:j]))
			i = j
		case c == '|':
			if i+1 < len(line) && line[i+1] == '|' {
				out = append(out, "||")
				i += 2
				continue
			}
			out = append(out, "|")
			i++
		case c == ':':
			if i+1 < len(line) && line[i+1] == ':' {
				out = append(out, "::")
				i += 2
				continue
			}
			out = append(out, ":")
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			// ★★ 空白**必须跳过，不能当 token**。
			//
			// R93 轮第一版把空白也 emit 成 token ⇒ 判据里所有「相邻性」判定
			// （「下一个 token 就是 `||`」「向前跳过 ident+`.` 后就是 `||`」）
			// 全部失效 ⇒ ⇒ 单元自检 4 条「期望 1 得 0」，而真实文件里只抓到
			// 恰好**无空白**的 `||t2.attstorage` 形态 ⇒ ⇒ **漏放**（有空格的真阳性看不见）。
			i++
		default:
			out = append(out, string(c))
			i++
		}
	}
	return out
}

// r93StripComment 按**文件扩展名**分派注释语法，返回去掉注释后的代码。
//
// ★★ 为什么必须分派：曾经用 `//` 当通用行注释标记 ⇒ **SQL 行被从中截断**
// ⇒ ⇒ 漏掉了 `09_final.sql:9` 的 `a.attalign` ⇒ ⇒ **漏放比误报危险**。
//
//	.sql → `--` 行注释；`/* … */` 块注释
//	.go  → `//` 行注释；`/* … */` 块注释
//	.sh/.py → `#` 行注释
func r93StripComment(src, ext string) string {
	var lineMark string
	switch ext {
	case ".sql":
		lineMark = "--"
	case ".go":
		lineMark = "//"
	case ".sh", ".py":
		lineMark = "#"
	default:
		lineMark = ""
	}

	var out strings.Builder
	out.Grow(len(src))
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		if inBlock {
			idx := strings.Index(line, "*/")
			if idx < 0 {
				out.WriteString("\n")
				continue
			}
			line = line[idx+2:]
			inBlock = false
		}
		for {
			start := strings.Index(line, "/*")
			if start < 0 {
				break
			}
			end := strings.Index(line[start:], "*/")
			if end < 0 {
				line = line[:start]
				inBlock = true
				break
			}
			line = line[:start] + " " + line[start+end+2:]
		}
		if lineMark != "" {
			if idx := strings.Index(line, lineMark); idx >= 0 {
				line = line[:idx]
			}
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// r93CharConcats 返回一行里所有「char 列裸拼接」的位置（列名 + 形态）。
func r93CharConcats(line string) []string {
	toks := r93Tokenize(line)
	var out []string
	for i, t := range toks {
		if !r93CharColumnSet[t] {
			continue
		}
		// ★★ 已带 cast ⇒ 不是裸拼接（`relkind::text`）。
		//    这条必须放在两个形态判断**之前**：否则 `|| c.relkind::text ||` 会被
		//    「右操作数」规则误判成裸拼接（它向前确实是 `||`）⇒ ⇒ 误报。
		if i+1 < len(toks) && toks[i+1] == "::" {
			continue
		}
		// ★ 右操作数形态：向前跳过「标识符 + `.`」后就是 `||`
		j := i - 1
		for j >= 1 && toks[j] == "." && r93IsIdentStart(toks[j-1]) {
			j -= 2
		}
		if j >= 0 && toks[j] == "||" {
			out = append(out, t+"（右操作数）")
			continue
		}
		// ★ 左操作数形态：下一个 token 就是 `||`
		// （`relkind::text ||` 的下一个 token 是 `::` ⇒ 上面已 continue ✔）
		if i+1 < len(toks) && toks[i+1] == "||" {
			out = append(out, t+"（左操作数）")
		}
	}
	return out
}

// r93IsIdentStart 判定 token 是不是标识符的**首字符**形态（用于跳「别名.」）。
func r93IsIdentStart(tok string) bool {
	if tok == "" {
		return false
	}
	c := tok[0]
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// TestNoCharColumnConcatenatedWithoutCast —— 全仓不得出现「`"char"` 列未加 cast 就参与 `||`」。
func TestNoCharColumnConcatenatedWithoutCast(t *testing.T) {
	// ★ 量具自检：白名单不能是空的（否则这道门会恒绿）。
	if len(r93CharColumns) < 30 {
		t.Fatalf("白名单只有 %d 个列名 ⇒ 判据口径失效（门会恒绿）", len(r93CharColumns))
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}

	type hit struct {
		rel  string
		line int
		what string
		text string
	}
	var hits []hit

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			// ★ 沿用仓内既有惯例：门目录自身豁免（destructive_target_guard_test.go 同款）
			if d.Name() == "ursmcheck" && filepath.Base(filepath.Dir(path)) == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(d.Name())
		switch ext {
		case ".sql", ".sh", ".go", ".py":
		default:
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		code := r93StripComment(string(raw), ext)
		rel, _ := filepath.Rel(root, path)
		for i, ln := range strings.Split(code, "\n") {
			if !strings.Contains(ln, "||") {
				continue
			}
			for _, what := range r93CharConcats(ln) {
				hits = append(hits, hit{rel, i + 1, what, strings.TrimSpace(ln)})
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}

	if len(hits) == 0 {
		return
	}
	for _, h := range hits {
		t.Errorf("%s:%d —— `%s` 是 PG 的 **\"char\"** 类型列，未加 cast 就参与了 `||` 拼接\n"+
			"    ⇒ 真库报 `operator is not unique: \"char\" || unknown`（假 psql 桩永远绿 ⇒ §82 栽过）\n"+
			"    ⇒ 修法 = 该列加 `::text`（仓内既有正确写法见 `pg_class.relkind::text`）\n"+
			"    代码：%s",
			h.rel, h.line, h.what, h.text)
	}
	t.Errorf("共 %d 处 `\"char\"` 列裸拼接（PG 17.10 实测该类型列共 %d 个）", len(hits), len(r93CharColumns))
}

// TestR93CharColumnStripCommentIsPerExtension —— 量具自检：注释分派必须真的生效。
//
// ★ 这条是**给量具自己上的门**：R93 轮第一版用 `//` 当通用行注释标记
// ⇒ SQL 行被从中截断 ⇒ 漏掉了一处真阳性 ⇒ ⇒ **漏放比误报危险**。
func TestR93CharColumnStripCommentIsPerExtension(t *testing.T) {
	sqlLine := "SELECT c.relkind || ' ' FROM pg_class c -- 注释里的 c.attstorage || 'x'"
	got := r93StripComment(sqlLine, ".sql")
	if strings.Contains(got, "attstorage") {
		t.Errorf("SQL 的 `--` 注释未被剥掉 ⇒ 会被误判为真阳性：%q", got)
	}
	if !strings.Contains(got, "c.relkind ||") {
		t.Errorf("SQL 代码部分被误删：%q", got)
	}

	goLine := "x := c.relkind || \"y\" // 注释里的 c.attstorage || \"z\""
	gotGo := r93StripComment(goLine, ".go")
	if strings.Contains(gotGo, "attstorage") {
		t.Errorf("Go 的 `//` 注释未被剥掉：%q", gotGo)
	}

	// ★ 关键：**SQL 里的 `//` 不能当注释**（它是除法，不是注释）
	slash := "SELECT 10 // 2 FROM t"
	gotSlash := r93StripComment(slash, ".sql")
	if !strings.Contains(gotSlash, "10 // 2") {
		t.Errorf("SQL 里的 `//` 被当成了注释 ⇒ 误删代码：%q", gotSlash)
	}
}

// TestR93CharConcatJudgment —— 判据自身的自检（正向 + 反向都要绿）。
func TestR93CharConcatJudgment(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
	}{
		{"裸的左操作数", "SELECT c.relkind || 'x' FROM pg_class c", 1},
		{"裸的右操作数", "SELECT 'x' || c.relkind FROM pg_class c", 1},
		{"带别名点号的右操作数", "SELECT 'x' || t.relkind FROM pg_class t", 1},
		{"加了 cast 就不算", "SELECT c.relkind::text || 'x' FROM pg_class c", 0},
		{"加了 cast 的右操作数也不算", "SELECT 'x' || c.relkind::text FROM pg_class c", 0},
		{"★ 加了 cast 还在 || 右边也不算（右操作数规则向前看是 ||）",
			"SELECT 'x' || c.relkind::text || 'y' FROM pg_class c", 0},
		{"★ 带别名前缀的右操作数且带 cast 也不算",
			"SELECT 'x' || t.attstorage::text || 'y' FROM pg_attribute t", 0},
		{"别的列不算", "SELECT c.relname || 'x' FROM pg_class c", 0},
		{"白名单外的列不算", "SELECT c.contype || 'x' FROM pg_constraint c", 1},
		{"标识符后缀不算", "SELECT myrelkind || 'x' FROM t", 0},
	}
	for _, tc := range cases {
		got := r93CharConcats(tc.line)
		if len(got) != tc.want {
			t.Errorf("%s：%q 期望 %d 处，实际 %d 处（%v）", tc.name, tc.line, tc.want, len(got), got)
		}
	}
}
