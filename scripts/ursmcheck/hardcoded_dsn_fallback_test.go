package ursm_test

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// R69/R70 轮的门：**「被当作『要连的库』的写死 DSN」必须登记，且必须已处置**。
//
// # 为什么要这道门（R70 轮实测）
//
// R69 轮查出：官方 gate runner 漏导出 21 个 DSN 变量名 ⇒ 真库腿**静默 SKIP 被记成绿**。
// ⇒ 但那只解决了「**env 没设**」的一档。本门盯的是**更隐蔽的一档**：
//
//	★ **「写死的 DSN」** —— 有些测试**根本不读 env**，直接写死一个库地址：
//	  `internal/outbox/writer_test.go:25`   `postgres://localhost/llm_gateway_test?sslmode=disable`
//	  `admin/usage_enhanced_test.go:492`     同上
//	  `bg/systemmonitor/metrics_collector_test.go:12` 同上
//	  `domains/credential/flusher_test.go:185` `postgres://kxuser:…@localhost:5434/llm_gateway`
//	⇒ ⇒ **env 覆盖对它们完全无效** ⇒ 事实源救不了。
//	⇒ ⇒ ★★ **本机实测**：它们连 `localhost/llm_gateway_test`（无口令 ⇒ 用 OS 用户名）
//	  ⇒ 认证失败 ⇒ **`t.Skipf` ⇒ SKIP ⇒ 被 `go test` 记成绿**。
//	⇒ ⇒ ★★★ 而**那个库在容器里是真实存在的** ⇒ ⇒ **在「本地恰好装了 PG 且该用户有权限」
//	  的机器上，它们会真的连上** ⇒ ⇒ `outbox` 那条是**写入型**（好在包在 `tx` 里且
//	  `defer tx.Rollback()` ⇒ 不留残留）⇒ ⇒ 真正的后果是**「静默红」或「连错库」**。
//
// # 判别轴：**不是「这个文件开不开 PG」，而是「这个字面量有没有被当作『要连的库』」**
//
//	★ 所以「故意不可达夹具」与「DSN 解析样本」都被排除在外：
//	  `pgxpool.New(ctx, "postgres://u:p@127.0.0.1:1/db")`（端口 1 = 必连不上，测失败路径）
//	  `pgx.ParseConfig("postgres://postgres@localhost/postgres?connect_timeout=1")`（只解析不连）
//	  `host=h dbname='my db' user=u`（解析器输入样本）
//	⇒ ⇒ **分类轴 = 该字面量的「角色」，不是它的长相**（同 R69 的教训）。
//
// # 门的三条判据
//
//	① **覆盖**：所有「被当作要连的库」的写死 DSN 必须在登记册里（漏登记 ⇒ 红）；
//	② **处置**：登记册里 `role=target` 的行**不许留 `pending`** ⇒ 门推动真正的修复；
//	③ **无死条目**：登记册里的行必须仍能在源码里找到（不许腐化）。

const (
	hardcodedDSNRegistry = "../../scripts/ursmcheck/hardcoded_dsn_targets.tsv"
)

var updateHardcodedDSN = flag.Bool("update", false, "重算登记册：新增行写 pending，保留已有行的标注")

// ── 扫描器 ───────────────────────────────────────────────────────────────────

var (
	reDSNShape = regexp.MustCompile(`^(postgres(?:ql)?://|host=\S+)`)
	// 连接构造函数：第一个参数是 ctx/conn 的，DSN 在后面的参数里。
	reConnCtor = regexp.MustCompile(`(?:pgxpool\.New|pgxpool\.Connect|pgx\.ConnectConfig|pgx\.Connect|sql\.Open)\s*\(`)
)

// goStringLiteralValuesWithLine 取每个字符串字面量的值与行号（1 起）。
func goStringLiteralValuesWithLine(src string) []struct {
	Value string
	Line  int
} {
	type lit = struct {
		Value string
		Line  int
	}
	var out []lit
	line := 1
	for i := 0; i < len(src); {
		switch {
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					line++
				}
				i++
			}
			i += 2
		case src[i] == '`':
			i++
			for i < len(src) && src[i] != '`' {
				if src[i] == '\n' {
					line++
				}
				i++
			}
			i++
		case src[i] == '"':
			i++
			var b strings.Builder
			for i < len(src) {
				if src[i] == '\\' && i+1 < len(src) {
					b.WriteByte(src[i+1])
					i += 2
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				if src[i] == '\n' {
					line++
				}
				b.WriteByte(src[i])
				i++
			}
			out = append(out, lit{b.String(), line})
		default:
			if src[i] == '\n' {
				line++
			}
			i++
		}
	}
	return out
}

// matchingParen 返回与 src[open] 处的 '(' 配对的 ')' 下标。
func matchingParen(src string, open int) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// hardcodedDSNTargets 找出「被当作要连的库」的写死 DSN。
//
// 覆盖两条路径：
//
//	① 字面量**直接**出现在连接构造函数的实参里；
//	② 字面量先赋给一个局部变量，再把该变量传进连接构造函数（**一层**数据流）。
func hardcodedDSNTargets(src string) map[string]int {
	// 局部变量 → DSN 字面量（只认「整条语句就是这个字面量」的简单赋值）
	varToDSN := map[string]string{}
	reAssign := regexp.MustCompile(`(?m)^\s*(\w+)\s*(?::=|=)\s*"([^"]*)"\s*$`)
	for _, m := range reAssign.FindAllStringSubmatch(src, -1) {
		if reDSNShape.MatchString(strings.TrimSpace(m[2])) {
			varToDSN[m[1]] = m[2]
		}
	}

	out := map[string]int{}
	// ★★ **必须先剥注释**（R70 轮自曝）：否则**注释掉的** `pgxpool.New(ctx, "postgres://…")`
	//    也会被匹配 ⇒ 门会把一条不存在的调用登记进去。
	//    剥注释用「保留行数」的版本 ⇒ 行号仍然对得上原文件。
	src = stripGoComments(src)
	for _, m := range reConnCtor.FindAllStringSubmatchIndex(src, -1) {
		open := m[1] - 1
		closeIdx := matchingParen(src, open)
		if closeIdx < 0 {
			continue
		}
		args := src[open+1 : closeIdx]
		// ① 实参里直接是字面量
		for _, lit := range goStringLiteralValuesWithLine(args) {
			if reDSNShape.MatchString(strings.TrimSpace(lit.Value)) {
				out[lit.Value] = lit.Line
			}
		}
		// ② 实参里是局部变量 ⇒ 追一层
		for name, dsn := range varToDSN {
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(args) {
				out[dsn] = 0
			}
		}
	}
	return out
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:10]
}

// ── 登记册 ───────────────────────────────────────────────────────────────────

type dsnRegistryRow struct {
	Path        string
	Literal     string
	Hash        string
	Role        string // target | probe | sample
	Disposition string // pending | env:<VAR> | skip-explicit
	Note        string
}

func readDSNRegistry(t *testing.T) []dsnRegistryRow {
	t.Helper()
	raw, err := os.ReadFile(hardcodedDSNRegistry)
	if err != nil {
		t.Fatalf("read %s: %v", hardcodedDSNRegistry, err)
	}
	var rows []dsnRegistryRow
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		for len(parts) < 6 {
			parts = append(parts, "")
		}
		rows = append(rows, dsnRegistryRow{
			Path: parts[0], Literal: parts[1], Hash: parts[2],
			Role: parts[3], Disposition: parts[4], Note: parts[5],
		})
	}
	return rows
}

func writeDSNRegistry(t *testing.T, rows []dsnRegistryRow) {
	t.Helper()
	var b strings.Builder
	b.WriteString("# 「被当作『要连的库』的写死 DSN」登记册（R70 轮建立）\n")
	b.WriteString("# 格式：path<TAB>literal<TAB>hash10<TAB>role<TAB>disposition<TAB>note\n")
	b.WriteString("#   role        : target | probe | sample\n")
	b.WriteString("#   disposition : pending | env:<VAR> | skip-explicit\n")
	b.WriteString("#   ★ role=target 的行**不许**留 pending（门 ②）⇒ 门推动真正的修复。\n")
	b.WriteString("# 用 `go test ./scripts/ursmcheck/ -run TestHardcodedDSNTargets -update` 重算骨架。\n")
	for _, r := range rows {
		b.WriteString(strings.Join([]string{r.Path, r.Literal, r.Hash, r.Role, r.Disposition, r.Note}, "\t"))
		b.WriteString("\n")
	}
	if err := os.WriteFile(hardcodedDSNRegistry, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", hardcodedDSNRegistry, err)
	}
}

// collectHardcodedDSNTargets 扫全仓测试文件，返回 (path, literal) → hash。
func collectHardcodedDSNTargets(t *testing.T) map[string]map[string]bool {
	t.Helper()
	root := filepath.Join("..", "..")
	found := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		if filepath.Base(path) == "hardcoded_dsn_fallback_test.go" {
			return nil // 门自己
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		targets := hardcodedDSNTargets(string(raw))
		if len(targets) == 0 {
			return nil
		}
		rel := filepath.ToSlash(path)
		if found[rel] == nil {
			found[rel] = map[string]bool{}
		}
		for lit := range targets {
			found[rel][lit] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// ── 门 ──────────────────────────────────────────────────────────────────────

func TestHardcodedDSNTargets(t *testing.T) {
	found := collectHardcodedDSNTargets(t)
	if len(found) == 0 {
		t.Fatal("一个写死 DSN 都没扫到 —— 扫描器失效（门会恒绿）")
	}
	rows := readDSNRegistry(t)

	// 索引：path+hash → row
	idx := map[string]*dsnRegistryRow{}
	for i := range rows {
		idx[rows[i].Path+"|"+rows[i].Hash] = &rows[i]
	}

	// ① 覆盖：源码里的每个 target 必须在册
	var missing []string
	seen := map[string]bool{}
	for path, lits := range found {
		for lit := range lits {
			h := shortHash(lit)
			seen[path+"|"+h] = true
			if _, ok := idx[path+"|"+h]; !ok {
				missing = append(missing, fmt.Sprintf("%s  %s", path, lit))
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("写死 DSN 被当作「要连的库」但**没在登记册里**：%s\n"+
			"  ⇒ 它连的库不由 env 决定 ⇒ 在任何机器上都可能连错库或静默 SKIP ⇒ 请登记并处置", m)
	}

	// ③ 无死条目：**probe/sample 档**的行必须还在源码里（防清单腐化）。
	//    ★ target 档且**已处置**的行**允许**「字面量已消失」—— 那正是修复的证据。
	for _, r := range rows {
		if !seen[r.Path+"|"+r.Hash] && r.Role != "target" {
			t.Errorf("登记册里的行已失效（源码里找不到了）：%s  %s", r.Path, r.Literal)
		}
	}

	// ② 处置：role=target 不许留 pending
	for _, r := range rows {
		switch r.Role {
		case "", "pending":
			t.Errorf("登记册行还没定档：%s  %s（role 必须是 target|probe|sample）", r.Path, r.Literal)
		case "target":
			if r.Disposition == "pending" || r.Disposition == "" {
				t.Errorf("登记册行 role=target 但**未处置**：%s  %s\n"+
					"  ⇒ 请改成 env:<变量名>（让它认事实源）或 skip-explicit（连不上就响亮 SKIP）", r.Path, r.Literal)
				continue
			}
			// ★★★ ③b **处置必须可验证**：disposition=env:VAR ⇒
			//     那个文件里必须**真的**出现这个变量名（否则「处置」只是一句口号）。
			if strings.HasPrefix(r.Disposition, "env:") {
				varName := strings.TrimPrefix(r.Disposition, "env:")
				raw, rerr := os.ReadFile(filepath.FromSlash(r.Path))
				if rerr != nil {
					t.Errorf("登记册行指向的文件读不到：%s", r.Path)
					continue
				}
				if !strings.Contains(string(raw), "\""+varName+"\"") {
					t.Errorf("登记册行声称已改成认 %s，但**该文件里没有这个变量名**：%s  %s\n"+
						"  ⇒ 「处置」必须可验证：要么真改，要么别在册里写 env:%s", varName, r.Path, r.Literal, varName)
				}
			}
		case "probe", "sample":
			// 正当档：不要求处置
		default:
			t.Errorf("登记册行 role 非法：%q（%s）", r.Role, r.Path)
		}
	}

	if *updateHardcodedDSN {
		// 重算：保留已有行的 role/disposition/note；新增行写 pending。
		merged := map[string]*dsnRegistryRow{}
		for i := range rows {
			r := rows[i]
			merged[r.Path+"|"+r.Hash] = &r
		}
		var out []dsnRegistryRow
		for path, lits := range found {
			for lit := range lits {
				h := shortHash(lit)
				if old, ok := merged[path+"|"+h]; ok {
					out = append(out, *old)
					continue
				}
				out = append(out, dsnRegistryRow{
					Path: path, Literal: lit, Hash: h,
					Role: "pending", Disposition: "pending", Note: "",
				})
			}
		}
		// ★★ **保留「已处置」的 target 行**：字面量已从源码里消失，
		//    那正是**修复的证据**（登记册是历史，不只是现状快照）。
		//    未处置的 target 行不保留 ⇒ 它会随字面量一起消失 ⇒ 门就看不见它了
		//    ⇒ ⇒ 所以**未处置时字面量一旦被删，门反而会失效** ⇒ 门必须盯住「已处置」这一列。
		inSource := map[string]bool{}
		for _, r := range out {
			inSource[r.Path+"|"+r.Hash] = true
		}
		for _, r := range rows {
			if inSource[r.Path+"|"+r.Hash] {
				continue
			}
			if r.Role == "target" && r.Disposition != "pending" && r.Disposition != "" {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Path != out[j].Path {
				return out[i].Path < out[j].Path
			}
			return out[i].Hash < out[j].Hash
		})
		writeDSNRegistry(t, out)
		t.Logf("登记册已重算：%d 行", len(out))
	}
}

// ── 扫描器自检（防恒绿门）──────────────────────────────────────────────────

func TestHardcodedDSNScannerSelfCheck(t *testing.T) {
	// ① 直接把字面量传给连接构造函数 ⇒ 必须抓到
	direct := `package x
func f() {
	pool, _ := pgxpool.New(ctx, "postgres://u:p@127.0.0.1:5432/db?sslmode=disable")
}`
	if got := hardcodedDSNTargets(direct); len(got) != 1 {
		t.Fatalf("直接实参没被抓到：%v", got)
	}

	// ② 先赋给局部变量再传进去 ⇒ 必须抓到（**一层**数据流）
	viaVar := `package x
func f() {
	dsn := "postgres://u:p@127.0.0.1:5432/db?sslmode=disable"
	pool, _ := pgxpool.New(ctx, dsn)
}`
	if got := hardcodedDSNTargets(viaVar); len(got) != 1 {
		t.Fatalf("经局部变量传递的没被抓到：%v", got)
	}

	// ③ 只解析不连接（ParseConfig）⇒ **不是** target
	parseOnly := `package x
func f() {
	cfg, _ := pgx.ParseConfig("postgres://postgres@localhost/postgres?connect_timeout=1")
}`
	if got := hardcodedDSNTargets(parseOnly); len(got) != 0 {
		t.Fatalf("只解析的字面量被误判成 target：%v", got)
	}

	// ④ 纯样本（不传给任何构造函数）⇒ 不是 target
	sample := `package x
const s = "host=h dbname='my db' user=u"`
	if got := hardcodedDSNTargets(sample); len(got) != 0 {
		t.Fatalf("样本被误判成 target：%v", got)
	}

	// ⑤ ★ 注释里的字面量 ⇒ 不是 target（剥注释必须有效）
	commented := `package x
func f() {
	// pool, _ := pgxpool.New(ctx, "postgres://u:p@127.0.0.1:5432/db")
	_ = 1
}`
	if got := hardcodedDSNTargets(commented); len(got) != 0 {
		t.Fatalf("注释里的字面量被误判成 target：%v", got)
	}

	// ⑥ ★ 字符串字面量里的 `//` 不能被当行注释（否则后半行的实参会被吃掉）
	urlInArgs := `package x
func f() {
	pool, _ := pgxpool.New(ctx, "postgres://u:p@127.0.0.1:5432/db?sslmode=disable")
}`
	if got := hardcodedDSNTargets(urlInArgs); len(got) != 1 {
		t.Fatalf("实参里的 // 被当成注释了：%v", got)
	}
}
