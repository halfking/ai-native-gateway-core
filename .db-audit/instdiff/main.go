// Command instdiff compares two llm-gateway database audit captures produced by
// sql/audit/2026-10-02-db-audit-collect.sql.
//
//	go run .db-audit/instdiff/main.go -a .db-audit/out/inst34.txt -b .db-audit/out/inst252.txt -label-a 34 -label-b 252
//
// Sections are split on the `===SECTION:<name>===` markers. Scalar sections are
// rendered side by side; list sections are compared as sets so that entries only
// present on one side are surfaced.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// scalar sections hold one "k|v" style record per line; list sections are free-form.
var scalarSections = map[string]bool{
	"META": true, "EXTENSIONS": true, "SCHEMA_COUNTS": true, "DB_SIZE": true,
	"STORAGE_MIX": true, "KEY_SETTINGS": true, "DB_ACTIVITY": true, "CHECKPOINTS": true,
	"PGSS_SUMMARY": true, "PGSS_WAL_JIT": true, "WAL_AND_REPLICA": true,
	"UNUSED_INDEXES": true, "NEVER_ANALYZED": true, "ARCHIVE_COLUMNAR": true,
	"COLUMNAR_GUCS": true, "END": true,
}

func parse(path string) (order []string, sec map[string][]string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sec = map[string][]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	cur := ""
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "===SECTION:") && strings.HasSuffix(t, "===") {
			cur = strings.TrimSuffix(strings.TrimPrefix(t, "===SECTION:"), "===")
			if _, ok := sec[cur]; !ok {
				order = append(order, cur)
			}
			continue
		}
		if cur == "" || t == "" {
			continue
		}
		// strip psql tuple/field decoration so both sides use the same shape
		sec[cur] = append(sec[cur], t)
	}
	return order, sec, sc.Err()
}

// key extracts the identity part of a list-section line (everything before the
// first '|' or the first ' = '), so reordering and size drift do not hide entries.
func key(line string) string {
	if i := strings.Index(line, "|"); i > 0 {
		return strings.TrimSpace(line[:i])
	}
	if i := strings.Index(line, " = "); i > 0 {
		return strings.TrimSpace(line[:i])
	}
	if i := strings.Index(line, "s | calls="); i > 0 {
		return strings.TrimSpace(line[:i])
	}
	return strings.TrimSpace(line)
}

func value(line string) string {
	if i := strings.Index(line, "|"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

// kvMap turns a scalar section into key->value. Sections are emitted either as
// "key|value" (META, SCHEMA_COUNTS) or as "name=value [source]" (KEY_SETTINGS),
// so both separators are accepted.
func kvMap(lines []string) map[string]string {
	m := map[string]string{}
	for _, l := range lines {
		if i := strings.Index(l, "|"); i >= 0 {
			m[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
			continue
		}
		if i := strings.Index(l, "="); i > 0 {
			m[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
			continue
		}
		m[l] = ""
	}
	return m
}

func main() {
	aPath := flag.String("a", "", "capture A (e.g. 34)")
	bPath := flag.String("b", "", "capture B (e.g. 252)")
	la := flag.String("label-a", "A", "label for A")
	lb := flag.String("label-b", "B", "label for B")
	out := flag.String("out", "", "write markdown report to this path (default stdout)")
	flag.Parse()

	if *aPath == "" || *bPath == "" {
		fmt.Fprintln(os.Stderr, "both -a and -b are required")
		os.Exit(2)
	}
	orderA, secA, err := parse(*aPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read A:", err)
		os.Exit(1)
	}
	orderB, secB, err := parse(*bPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read B:", err)
		os.Exit(1)
	}

	var w *bufio.Writer
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "create out:", err)
			os.Exit(1)
		}
		defer f.Close()
		w = bufio.NewWriter(f)
		defer w.Flush()
	} else {
		w = bufio.NewWriter(os.Stdout)
	}

	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }

	// section order: A's order, then any B-only sections
	seen := map[string]bool{}
	var order []string
	for _, s := range orderA {
		order = append(order, s)
		seen[s] = true
	}
	for _, s := range orderB {
		if !seen[s] {
			order = append(order, s)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })

	p("# llm-gateway 数据库实例对比\n\n")
	p("- A: `%s` (`%s`)\n- B: `%s` (`%s`)\n", *la, *aPath, *lb, *bPath)
	p("- 采集脚本：`sql/audit/2026-10-02-db-audit-collect.sql`\n\n")

	missing := 0
	for _, s := range order {
		linesA, okA := secA[s]
		linesB, okB := secB[s]
		switch {
		case !okA && !okB:
			continue
		case !okA:
			missing++
			// A 缺该节 => 有该节的是 B,标签必须是 lb/la。原实现这里写反了,
			// 会让读者把"谁缺该节"看反。
			p("## %s\n\n> ⚠️ 只有 `%s` 有该节（`%s` 缺失），无法比较。\n\n", s, *lb, *la)
			continue
		case !okB:
			missing++
			p("## %s\n\n> ⚠️ 只有 `%s` 有该节（`%s` 缺失），无法比较。\n\n", s, *la, *lb)
			continue
		}

		if scalarSections[s] {
			ma, mb := kvMap(linesA), kvMap(linesB)
			var keys []string
			ks := map[string]bool{}
			for k := range ma {
				ks[k] = true
			}
			for k := range mb {
				ks[k] = true
			}
			for k := range ks {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var rows []string
			for _, k := range keys {
				va, oka := ma[k]
				vb, okb := mb[k]
				if oka && okb && va == vb {
					continue
				}
				if !oka {
					va = "—（本侧无）"
				}
				if !okb {
					vb = "—（对侧无）"
				}
				rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", k, va, vb))
			}
			if len(rows) == 0 {
				p("## %s\n\n一致（%d 项）。\n\n", s, len(keys))
				continue
			}
			p("## %s\n\n存在差异的项：\n\n| 项 | %s | %s |\n|---|---|---|\n", s, *la, *lb)
			for _, r := range rows {
				p("%s\n", r)
			}
			p("\n")
			continue
		}

		// list section: set difference on the identity prefix
		ka, kb := map[string]string{}, map[string]string{}
		for _, l := range linesA {
			ka[key(l)] = l
		}
		for _, l := range linesB {
			kb[key(l)] = l
		}
		var onlyA, onlyB []string
		for k := range ka {
			if _, ok := kb[k]; !ok {
				onlyA = append(onlyA, ka[k])
			}
		}
		for k := range kb {
			if _, ok := ka[k]; !ok {
				onlyB = append(onlyB, kb[k])
			}
		}
		sort.Strings(onlyA)
		sort.Strings(onlyB)
		p("## %s\n\n%s 共 %d 条，%s 共 %d 条。", s, *la, len(ka), *lb, len(kb))
		if len(onlyA) == 0 && len(onlyB) == 0 {
			p("**条目集合完全一致**（仅数值随时间漂移）。\n\n")
			continue
		}
		p("\n")
		if len(onlyA) > 0 {
			p("**仅 %s 有（%d 条）：**\n\n", *la, len(onlyA))
			for _, l := range onlyA {
				p("- `%s`\n", l)
			}
			p("\n")
		}
		if len(onlyB) > 0 {
			p("**仅 %s 有（%d 条）：**\n\n", *lb, len(onlyB))
			for _, l := range onlyB {
				p("- `%s`\n", l)
			}
			p("\n")
		}
	}
	if missing > 0 {
		p("\n---\n\n有 %d 个分节在两侧都缺失或单侧缺失，采集可能未跑全，请检查 `COLLECT_EXIT`。\n", missing)
	}
}
