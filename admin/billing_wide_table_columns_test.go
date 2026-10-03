// billing_wide_table_columns_test.go —— 197 号审计（全面审计v3/2026-10-03/197）：
// 把 33da7e609 的「单文件 + 手工列清单」回归门扩成**族级门**。
//
// 33da7e609（2026-10-03）修掉了 usage_enhanced.go 的三处幻影列引用
// （ul.work_type / ul.gw_session_id / ul.compression_strategy），并留下
// usage_ledger_sourceless_columns_test.go。它的判据形状是对的，但覆盖面有两处
// 天花板，本门补的就是这两处：
//
//	① **文件级**：只扫 admin/usage_enhanced.go 一个文件。而全仓读
//	   usage_ledger / usage_ledger_with_current_month / usage_ledger_hot 的
//	   非测试 .go 文件有 **29 个**（admin / bg / domains / maas / internal /
//	   metrics / cmd）。今天在 usage.go 或 bg/ledger_reconciliation.go 里再写一个
//	   幻影列，**没有任何门会响**。
//	② **手工列清单**：sourcelessColumns 是人手维护的常量。加一列迁移它不会更新，
//	   删一列它也不会更新 —— 清单与 schema 漂移时，门要么恒红要么恒绿。
//
// 本门的两处设计决定，都是从 33da7e609 的两条教训推出来的：
//   - **列集从 schema 派生**（基线 CREATE TABLE + 跨行 ALTER ADD COLUMN），
//     不手维护 ⇒ 迁移加列它自动变绿，删列它自动变红。
//   - **去 Go 注释但保留 raw string**（复用 stripGoCommentsKeepLines）——
//     SQL 住在反引号 raw string 里，必须留着；而 usage_enhanced.go:284-287 的
//     移除理由注释里**就写着** `COUNT(DISTINCT ul.gw_session_id)`，不去注释的门
//     必然误报它。一条会误报的门比没有门更坏。
package admin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// billingWideTableRelations are the read-side relations whose column set this
// gate derives and checks. The cross-month view is `SELECT * FROM usage_ledger
// UNION ALL SELECT * FROM usage_ledger_<month>`, so it inherits the table's
// columns exactly; _hot is a separate heap table that ALTERs receive in lockstep
// (migration 736's own comment states this), so one derived set serves all three.
var billingWideTableRelations = []string{
	"usage_ledger",
	"usage_ledger_with_current_month",
	"usage_ledger_hot",
}

var (
	// usageLedgerCreateTable matches the authoritative baseline definition. The
	// column list is read out of the parenthesised body, so it is the schema's
	// own answer rather than a hand-kept copy of it.
	usageLedgerCreateTable = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:public\.)?usage_ledger\s*\((.*?)\n\);`)

	// usageLedgerAddColumn must be whitespace-tolerant across newlines: migration
	// 736 writes "ALTER TABLE usage_ledger\n  ADD COLUMN IF NOT EXISTS
	// rate_multiplier ...". A single-line pattern silently drops that column,
	// which shrinks the derived set and turns every use of it into a false
	// positive — the failure looks like "the detector got noisier" instead of
	// "the derivation is wrong".
	usageLedgerAddColumn = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:public\.)?usage_ledger\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)

	// billingAliasBind captures the alias a FROM/JOIN clause assigns to one of
	// the wide relations. Trailing SQL keywords are filtered separately so
	// "FROM usage_ledger WHERE ..." does not bind the alias "where".
	billingAliasBind = regexp.MustCompile(`(?is)(?:FROM|JOIN)\s+(?:public\.)?(usage_ledger(?:_with_current_month|_hot)?)\s+(?:AS\s+)?([a-z_][a-z0-9_]*)`)

	// billingColumnUse matches a qualified column reference on a bound alias.
	billingColumnUse = func(alias string) *regexp.Regexp {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\.([a-z_][a-z0-9_]*)`)
	}
)

var billingNonAliasWords = map[string]bool{
	"where": true, "group": true, "order": true, "limit": true, "offset": true,
	"on": true, "and": true, "or": true, "select": true, "having": true,
	"union": true, "left": true, "inner": true, "full": true, "cross": true,
	"join": true, "using": true, "fetch": true, "for": true, "set": true,
	"as": true, "values": true, "returning": true, "window": true,
}

// billingColsOnce memoises the derivation. Walking the repository's SQL twice
// (once per test) cost ~29s, which is the kind of runtime that gets a guard
// switched off in CI rather than fixed. The schema cannot change between two
// tests in one run.
var (
	billingColsOnce  sync.Once
	billingColsCache []string
	billingColsErr   string
)

func billingWideTableColumns(t *testing.T) []string {
	t.Helper()
	billingColsOnce.Do(func() {
		cols, err := deriveBillingWideTableColumns(t)
		if err != nil {
			billingColsErr = err.Error()
			return
		}
		billingColsCache = cols
	})
	if billingColsErr != "" {
		t.Fatalf("derive billing wide table columns: %s", billingColsErr)
	}
	return billingColsCache
}

// deriveBillingWideTableColumns reads the wide table's real column set out of
// the repository's own SQL: the baseline CREATE TABLE plus every ADD COLUMN
// migration. Returns the sorted set.
func deriveBillingWideTableColumns(t *testing.T) ([]string, error) {
	root := repoRootFromCaller(t)
	cols := map[string]bool{}

	baseline := filepath.Join(root, "deploy", "sql", "schemas", "baseline", "01-schema.sql")
	body, err := os.ReadFile(baseline)
	if err != nil {
		return nil, fmt.Errorf("read baseline schema: %w", err)
	}
	m := usageLedgerCreateTable.FindSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("baseline schema no longer defines CREATE TABLE public.usage_ledger; " +
			"this gate's derivation must be updated (it must not silently degrade to an empty set)")
	}
	for _, line := range strings.Split(string(m[1]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		// Column lines look like "  cost_usd numeric NOT NULL DEFAULT 0," —
		// table-level constraints (PRIMARY KEY, CONSTRAINT, UNIQUE) start with
		// a keyword and must not be read as columns.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(fields[0], ","), " ")
		if isTableLevelConstraintKeyword(name) {
			continue
		}
		if !isPlainIdentifier(name) {
			continue
		}
		cols[name] = true
	}

	// ADD COLUMN may live anywhere under sql/ or the installer's embedded
	// migrations, and may be split across lines.
	alters := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "docs", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".sql") || strings.HasSuffix(d.Name(), ".down.sql") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, hit := range usageLedgerAddColumn.FindAllSubmatch(src, -1) {
			cols[string(hit[1])] = true
			alters++
		}
		return nil
	})
	if alters == 0 {
		// Derivation that never saw an ADD COLUMN is either correct (the table
		// has none) or broken. Pin the known one so a broken regex cannot pass
		// as "clean".
		return nil, fmt.Errorf("no ADD COLUMN migration found for usage_ledger; migration 736 adds rate_multiplier, so the derivation pattern has regressed")
	}
	if !cols["rate_multiplier"] {
		return nil, fmt.Errorf("derived column set is missing rate_multiplier (migration 736); got %v", sortedBillingCols(cols))
	}

	out := sortedBillingCols(cols)
	if len(out) < 10 {
		return nil, fmt.Errorf("derived column set looks truncated (%d columns): %v", len(out), out)
	}
	return out, nil
}

func isTableLevelConstraintKeyword(name string) bool {
	switch strings.ToUpper(name) {
	case "PRIMARY", "FOREIGN", "CONSTRAINT", "UNIQUE", "CHECK", "EXCLUDE", "LIKE":
		return true
	}
	return false
}

func isPlainIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func sortedBillingCols(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scanBillingSourcelessRefs reports, per file, every "<alias>.<column>" where the
// alias is bound to a wide relation and the column is not in the derived set.
//
// Comments are stripped first (SQL lives in raw strings, which the stripper
// preserves) — usage_enhanced.go:284-287 documents the removed
// COUNT(DISTINCT ul.gw_session_id) in a comment, and a scanner that reads it
// would flag correct code.
func scanBillingSourcelessRefs(src string, known map[string]bool) []string {
	code := stripGoCommentsKeepLines(src)
	var findings []string
	aliases := map[string]bool{}
	for _, m := range billingAliasBind.FindAllStringSubmatch(code, -1) {
		alias := m[2]
		if billingNonAliasWords[strings.ToLower(alias)] {
			continue
		}
		aliases[alias] = true
	}
	names := make([]string, 0, len(aliases))
	for a := range aliases {
		names = append(names, a)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	for _, alias := range names {
		for _, m := range billingColumnUse(alias).FindAllStringSubmatch(code, -1) {
			col := m[1]
			if known[col] || seen[alias+"."+col] {
				continue
			}
			seen[alias+"."+col] = true
			findings = append(findings, alias+"."+col)
		}
	}
	sort.Strings(findings)
	return findings
}

// TestBillingWideTableColumns_SourcelessColumnRefs is the gate: no non-test Go
// file may qualify a column on a wide-table alias that the schema does not have.
//
// Why this is P2-grade and not cosmetic: 33da7e609 showed that such a reference
// is a permanent 42703, and IsSchemaBehindError (admin/dashboard_degrade.go:59)
// downgrades it to **200 + all zeros** — the operator sees "no spend this month"
// while the money was spent. That is worse than a 500, and no runtime signal
// distinguishes the two.
func TestBillingWideTableColumns_SourcelessColumnRefs(t *testing.T) {
	known := map[string]bool{}
	for _, c := range billingWideTableColumns(t) {
		known[c] = true
	}
	root := repoRootFromCaller(t)

	skipDir := map[string]bool{".git": true, "docs": true, "node_modules": true, "vendor": true}
	scanned, offenders := 0, 0

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// Only files that actually read the wide table are candidates; the
		// partition manager and TTL settings name it without querying columns.
		if !strings.Contains(string(src), "usage_ledger") {
			return nil
		}
		scanned++
		findings := scanBillingSourcelessRefs(string(src), known)
		if len(findings) == 0 {
			return nil
		}
		offenders++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		t.Errorf("%s references column(s) absent from the billing wide table: %s\n"+
			"  a 42703 here is downgraded by IsSchemaBehindError into 200 + all zeros, "+
			"so the endpoint silently reports \"no data\" forever (33da7e609)",
			rel, strings.Join(findings, ", "))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if scanned < 10 {
		t.Fatalf("only %d files reference usage_ledger; the walker's filters have regressed "+
			"(197号 measured 29 non-test readers)", scanned)
	}
	t.Logf("scanned %d non-test readers of the billing wide table, %d offenders", scanned, offenders)
}

// TestBillingWideTableColumns_DetectorRejectsTheOriginalBug is this gate's own
// premise. Without it, a detector that never fires would look identical to a
// clean tree. The fixture is the pre-fix query shape from b9a8baba4 via 33da7e609.
func TestBillingWideTableColumns_DetectorRejectsTheOriginalBug(t *testing.T) {
	known := map[string]bool{}
	for _, c := range billingWideTableColumns(t) {
		known[c] = true
	}

	// (a) The three references that shipped as 42703.
	buggy := "SELECT COUNT(DISTINCT ul.gw_session_id) FROM usage_ledger_with_current_month ul"
	want := map[string]bool{"ul.gw_session_id": true, "ul.work_type": true, "ul.compression_strategy": true}
	got := scanBillingSourcelessRefs(buggy, known)
	if len(got) != 1 || got[0] != "ul.gw_session_id" {
		t.Fatalf("detector missed the original bug: got %v", got)
	}
	for _, frag := range []string{
		"SELECT ul.work_type FROM usage_ledger ul",
		"SELECT ul.compression_strategy FROM usage_ledger_with_current_month ul",
	} {
		if f := scanBillingSourcelessRefs(frag, known); len(f) != 1 || !want[f[0]] {
			t.Fatalf("detector missed %q: got %v", frag, f)
		}
	}

	// (b) Reverse control: a column the table really has must pass, otherwise
	// the gate is just "flag everything".
	ok := []string{
		"SELECT ul.cost_usd, ul.total_tokens FROM usage_ledger ul",
		"SELECT ul.rate_multiplier FROM usage_ledger_with_current_month ul", // added by migration 736
		"SELECT COUNT(DISTINCT l.gw_session_id) FROM request_logs l",        // right column, right table
	}
	for _, frag := range ok {
		if f := scanBillingSourcelessRefs(frag, known); len(f) != 0 {
			t.Fatalf("detector false-positives on valid SQL %q: %v", frag, f)
		}
	}

	// (c) Comment control: the same reference inside a comment is not a
	// violation. usage_enhanced.go:284-287 is the real-world instance.
	commented := "// 原实现 COUNT(DISTINCT ul.gw_session_id) 必然 42703\n" +
		"SELECT ul.cost_usd FROM usage_ledger ul"
	if f := scanBillingSourcelessRefs(commented, known); len(f) != 0 {
		t.Fatalf("detector must ignore commented references, got %v", f)
	}
}
