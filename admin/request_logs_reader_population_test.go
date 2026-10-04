package admin

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// populationEntry is one inventoried file, classified from the SQL string
// literals it actually contains.
type populationEntry struct {
	file    string
	proxy   int // the inventory's line-regex call-site count
	v1Lits  int
	viewLit int
	cols    int
	verdict db.RetirementRepointVerdict
}

// viewRelationRe matches the canonical view, its wrapper views, and the bodies
// family's wrapper view.
//
// The bodies one is not optional: without it `admin/session_bodies_batch.go`
// (`FROM request_logs_bodies_with_current_month rb`) matched neither family and
// fell through the classifier's default branch. A classifier with a default
// branch is a guess, and the "nothing matched" count is asserted to be 0.
var viewRelationRe = regexp.MustCompile(
	`(?i)\brequest_logs(_bodies)?_with_current_month(_without_[a-z_]+)?\b`)

// v1BaseTableRe matches the v1 base tables. The `\b` after the optional suffix
// is what keeps `request_logs_with_current_month` out of this family — the exact
// place §9.167's alias bug lived — so it is asserted through the relation-name
// partition below rather than trusted.
//
// The suffix list is a closed allowlist, not a wildcard. A wildcard
// (`(_[a-z0-9_]+)?`) would swallow every view name, because `_with_current_month`
// is a syntactically valid suffix; that is the same over-match that made a naive
// drop-set empty the view chain. A name outside this list falls through to
// neither family and is reported by the `unclassified` gate, which is the
// intended safety net: a new v1 table cannot be added without the gate having an
// opinion about it.
var v1BaseTableRe = regexp.MustCompile(
	`(?i)\bfrom\s+(public\.)?request_logs(_hot|_bodies|_bodies_hot|_archive)?\b`)

// TestReaderPopulationGroundTruth (audit §9.172, settles D20-b).
func TestReaderPopulationGroundTruth(t *testing.T) {
	root := repoRootFromCaller(t)

	assessed := map[string]bool{}
	for f := range retirementBreakers {
		assessed[f] = true
	}
	for f := range retirementReattributed {
		assessed[f] = true
	}

	var all []populationEntry
	var unclassified, blindSpot, unjudged, registryDrift []string
	proxyV1Files, commentFixed := 0, 0

	for f, proxy := range requestLogsReadInventory {
		abs := filepath.Join(root, f)
		c := populationEntry{file: f, proxy: proxy}

		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v — the inventory names a file that cannot be built", f, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			raw, err := strconv.Unquote(lit.Value)
			if err != nil {
				// A backtick raw literal is not strconv-decodable; slice it.
				if len(lit.Value) >= 2 && lit.Value[0] == '`' {
					raw = lit.Value[1 : len(lit.Value)-1]
				} else {
					return true
				}
			}
			// SQL comments inside a string literal are not relations, in any
			// language. domains/streaming/model_alternatives.go reads exactly one
			// relation (`FROM request_logs_hot`) and only *mentions*
			// `request_logs_with_current_month` in a `--` comment carried inside
			// the SQL string; classifying that as a view read put a v1-only
			// reader in the "兼读" bucket. Same class of error as §9.167's
			// short-name-first alternation, one layer down.
			stripped := stripSQLComments(raw)
			hitV1 := v1BaseTableRe.MatchString(stripped)
			hitView := viewRelationRe.MatchString(stripped)
			if v1BaseTableRe.MatchString(raw) != hitV1 || viewRelationRe.MatchString(raw) != hitView {
				// Measured, not gated: making stripSQLComments a no-op stays green,
				// because the only assertions here are the family partition and
				// the unclassified count, and a comment-borne relation name does
				// not change either. This counter is here so the effect is at
				// least visible instead of being an unstated assumption.
				commentFixed++
			}
			if hitV1 {
				c.v1Lits++
			}
			if hitView {
				c.viewLit++
			}
			return true
		})

		if c.v1Lits == 0 && c.viewLit == 0 {
			unclassified = append(unclassified, f)
			all = append(all, c)
			continue
		}
		if c.v1Lits == 0 && c.proxy > 0 {
			// The proxy counted a v1 read where the AST finds none. Not an
			// error in either tool — it is the measurement §9.172 is about.
			proxyV1Files++
		}

		if c.v1Lits > 0 {
			cols := map[string]bool{}
			for _, l := range extractV1ReadingLiterals(t, abs) {
				for col := range l.allColumns {
					cols[col] = true
				}
			}
			c.cols = len(cols)
			names := make([]string, 0, len(cols))
			for col := range cols {
				names = append(names, col)
			}
			sort.Strings(names)
			c.verdict = db.RetirementRepointVerdictFor(names)
			if c.verdict == db.RepointNoColumnsMeasured {
				// Not an instrument failure. A reader can legitimately read v1
				// while naming **no** canonical column statically — the columns
				// arrive in a runtime-assembled WHERE. `admin/provider_models.go`
				// is the shape: `fmt.Sprintf("SELECT COUNT(*) FROM request_logs rl
				// WHERE %s", where)`. That is the §9.49 dynamic-column blind spot
				// (D14-c), and calling it a failure would be inventing a defect to
				// justify a red test.
				blindSpot = append(blindSpot, f)
			}
			if !assessed[f] {
				unjudged = append(unjudged, f)
			}
		}
		// Only v1 readers belong in the repoint work list. A view reader is not
		// a repoint candidate, so counting it as "unjudged" made this counter
		// exceed the population and the log printed a negative — which is the
		// cheapest possible signal that a count is wrong, and it shipped through
		// a compiler without complaint.
		all = append(all, c)
	}

	for f := range assessed {
		if _, ok := requestLogsReadInventory[f]; !ok {
			registryDrift = append(registryDrift, f)
		}
	}

	var familyFaults []string
	// The view family is asserted to classify every real relation, not a
	// hand-picked sample: v1 base tables must land in the v1 family, and every
	// wrapper view in the schema SSOT must land in the view family and in no
	// other. "Neither" is the default-branch failure — the shape that let
	// request_logs_bodies_with_current_month fall out of both families in
	// §9.172's first draft — so it is an error, not a skip.
	chain := viewChainNames(t)
	if len(chain) == 0 {
		t.Fatalf("view chain derived as empty (sources: db/request_logs_view_schema.go + " +
			"sql/schema/01-schema.sql, minus forward-migration drops) — a pattern or path change " +
			"would make the partition check below vacuous")
	}
	for _, name := range append(append([]string{}, v1BaseTableNames...), chain...) {
		inV1 := v1BaseTableRe.MatchString("FROM " + name)
		inView := viewRelationRe.MatchString("FROM " + name)
		switch {
		case inV1 && inView:
			familyFaults = append(familyFaults, name+" 被两族同时认领")
		case inV1 && !inView:
			if !containsName(v1BaseTableNames, name) {
				familyFaults = append(familyFaults, name+"（视图链成员）被判为 v1 底表")
			}
		case !inV1 && inView:
			if containsName(v1BaseTableNames, name) {
				familyFaults = append(familyFaults, name+"（v1 底表）被判为视图")
			}
		default:
			familyFaults = append(familyFaults, name+" 两族都不匹配（分类器的默认分支）")
		}
	}
	t.Logf("族名分区：v1 底表 %d + 视图链 %d（推导自视图 schema + 前向迁移重放）= %d 个关系名",
		len(v1BaseTableNames), len(chain), len(v1BaseTableNames)+len(chain))

	sort.Strings(unclassified)
	sort.Strings(blindSpot)
	sort.Strings(unjudged)
	sort.Strings(registryDrift)

	var v1Only, viewOnly, both, v1Files, sumV1, sumView int
	for _, c := range all {
		switch {
		case c.v1Lits > 0 && c.viewLit > 0:
			both++
			v1Files++
		case c.v1Lits > 0:
			v1Only++
			v1Files++
		case c.viewLit > 0:
			viewOnly++
		}
		sumV1 += c.v1Lits
		sumView += c.viewLit
	}

	t.Logf("inventory=%d | v1底表读方=%d (仅v1=%d 兼读=%d) | 仅视图读方=%d | 未归类=%d",
		len(all), v1Files, v1Only, both, viewOnly, len(unclassified))
	t.Logf("SQL 字面量：v1=%d 视图=%d 合计=%d | 行正则代理合计=%d | 代理把 %d 个文件当成 v1 读方而 AST 说不是",
		sumV1, sumView, sumV1+sumView, sumProxy(all), proxyV1Files)
	t.Logf("v1 读方中已判定=%d 未判定=%d（未判定是工作清单，见 D20-c）", v1Files-len(unjudged), len(unjudged))
	t.Logf("SQL 字面量内的注释使族判定发生改变=%d 处（剥离逻辑只测量不设门，去掉它本门仍绿）", commentFixed)

	// What the 62 actually are, by verdict. Not a gate: the verdict mix moves
	// as readers get assessed, and §9.172 changed no production code. It is here
	// because "62 v1 readers" reads like a total and is not — the number that
	// blocks retirement is the verdict mix, not the file count. The eight
	// verdicts are printed as they are rather than folded into a "blocking"
	// bucket, because a bucket of my own invention is exactly the kind of
	// number that gets quoted later as if it came from the classifier.
	byVerdict := map[db.RetirementRepointVerdict]int{}
	colsTotal := 0
	for _, c := range all {
		if c.v1Lits > 0 {
			byVerdict[c.verdict]++
			colsTotal += c.cols
		}
	}
	parts := make([]string, 0, len(byVerdict))
	for v, n := range byVerdict {
		parts = append(parts, fmt.Sprintf("%s=%d", v, n))
	}
	sort.Strings(parts)
	t.Logf("v1 读方判定分布（%d 个 v1 读方，静态引用契约列合计 %d 个）：%s",
		v1Files, colsTotal, strings.Join(parts, " "))

	if len(unclassified) > 0 {
		t.Errorf("%d inventory entries match neither a v1 base table nor a view: %s — a "+
			"classifier with a default branch is a guess, and the third relation family "+
			"(request_logs_bodies_with_current_month) was exactly that branch", len(unclassified),
			strings.Join(unclassified, " "))
	}
	if len(blindSpot) > 0 {
		t.Logf("v1 reader(s) naming no canonical column statically (%d) — §9.49 动态列名盲区 "+
			"/ D14-c，本门不判为故障: %s", len(blindSpot), strings.Join(blindSpot, " "))
	}
	if len(registryDrift) > 0 {
		t.Errorf("%d registered file(s) are not in requestLogsReadInventory: %s", len(registryDrift),
			strings.Join(registryDrift, " "))
	}
	if len(familyFaults) > 0 {
		t.Errorf("the two relation families are not cleanly separated — %s. A name claimed by both "+
			"families is the shape §9.167's short-name-first alternation had: a v1 read was recognised "+
			"as a view read (or a relation fell through to no family at all), and the population this "+
			"file measures stops meaning \"files that read the v1 base tables\".", strings.Join(familyFaults, "; "))
	}
	// The unjudged list is the D20-c work list, and it is the one count here
	// that is a ratchet rather than a reading: assessing a file only ever
	// removes it. So it is asserted, while every other count in this file is
	// deliberately not.
	//
	// What this catches is a *new* v1 reader landing in the inventory without
	// anyone assessing it — which is the failure mode that made §9.162's
	// "16 assessed files" read like a total. It does not enforce that anyone
	// reaches zero; that is D20-c, and it is the owner's call.
	if len(unjudged) > unjudgedBaseline {
		t.Errorf("未判定 v1 读方 = %d，超过基线 %d（只许缩小）。清单里新出现一个 v1 读方时，"+
			"它的退役风险必须被登记，不能靠「清单变大了」蒙过去。", len(unjudged), unjudgedBaseline)
	}
	// The count above is not enough on its own, and this is the same mistake the
	// ledger gate made in §9.173: a count is satisfied by a swap. Removing one
	// unjudged file while adding another leaves it at 47 and the ratchet silent.
	// So the baseline is a **set**, and membership is asserted, not just size.
	//
	// The check is deliberately one-directional (current ⊆ baseline). Dropping a
	// file from the baseline when it gets assessed is the normal way the list
	// shrinks; forgetting to is harmless here and caught by the size gate.
	var appeared []string
	for _, f := range unjudged {
		if !unjudgedBaselineFiles[f] {
			appeared = append(appeared, f)
		}
	}
	if len(appeared) > 0 {
		sort.Strings(appeared)
		t.Errorf("以下 v1 读方不在未判定基线里，共 %d 个：%s —— 清单总长未变（一个被判定、"+
			"一个被新增）时只看数量会静默放过；每个新读方都必须走一次判定并登记。",
			len(appeared), strings.Join(appeared, " "))
	}
	// Not asserted: the unjudged count against zero. It is the work list.
}

// unjudgedBaseline is the D20-c work-list size as of §9.174 (origin/main
// 9187555f2): 47 v1 readers that no assessment has ever run against. Lower it
// when the list shrinks; raising it needs a written reason, because raising it
// means a new reader arrived unassessed.
//
// The baseline is a second number living outside the instrument, so it gets the
// same treatment as every other hand-maintained list in this audit: it is
// asserted to be consistent with what the instrument measured on the same
// commit, rather than being trusted.
var unjudgedBaseline = 47

// unjudgedBaselineFiles is the membership that goes with unjudgedBaseline.
// A set, not a count: see the swap case in TestReaderPopulationGroundTruth.
var unjudgedBaselineFiles = map[string]bool{
	"admin/analytics.go":                            true,
	"admin/auto_route_outcome_freshness.go":         true,
	"admin/body_resolver.go":                        true,
	"admin/credential_success_rate.go":              true,
	"admin/data_lifecycle.go":                       true,
	"admin/data_lifecycle_blobs.go":                 true,
	"admin/data_lifecycle_metrics.go":               true,
	"admin/diagnostics_credential.go":               true,
	"admin/provider_diagnose.go":                    true,
	"admin/provider_models.go":                      true,
	"admin/quality_correlations.go":                 true,
	"admin/request_trace.go":                        true,
	"admin/session_detail_v2.go":                    true,
	"admin/session_sanitize_matches.go":             true,
	"admin/session_tenant.go":                       true,
	"admin/telemetry.go":                            true,
	"admin/tenants.go":                              true,
	"admin/unified_detail.go":                       true,
	"autoroute/recommend_v2.go":                     true,
	"bg/auto_route_affinity_worker.go":              true,
	"bg/integrity_fingerprint_probe.go":             true,
	"bg/ledger_reconciliation.go":                   true,
	"bg/lite_retention_worker.go":                   true,
	"bg/model_tier.go":                              true,
	"cmd/compression-bench/main.go":                 true,
	"cmd/gateway/main_v3_wiring.go":                 true,
	"cmd/gateway/output_compliance_control.go":      true,
	"cmd/gateway/waterfall_by_request.go":           true,
	"cmd/gateway/waterfall_db.go":                   true,
	"cmd/scenario_driver/main.go":                   true,
	"cmd/tools/backfill_session_bodies/main.go":     true,
	"cmd/tools/validate_sessions_v2/loader.go":      true,
	"cmd/traffic-replay/main.go":                    true,
	"discovery/discovery.go":                        true,
	"domains/analysis/optimizer.go":                 true,
	"domains/analysis/request_summary.go":           true,
	"domains/credentialstate/popularity_tracker.go": true,
	"domains/hooks/goal/history_store.go":           true,
	"domains/providerprofile/adapters.go":           true,
	"domains/sessionforensics/export.go":            true,
	"domains/streaming/anomaly_harvester.go":        true,
	"internal/quality/minute_aggregator.go":         true,
	"internal/summarystore/store.go":                true,
	"internal/trace/trace.go":                       true,
	"storage/sqlite/request_log_store.go":           true,
	"tests/session_audit/cmd/audit-test/main.go":    true,
	"tests/test_popularity_tracker.go":              true,
}

// viewChainNames derives the canonical view chain from the schema SSOT rather
// than from a hand-kept list. A hand-kept list rots silently: §9.172's first
// draft carried a probe for `request_logs_hot_with_current_month`, a relation
// that exists nowhere in the repo — an invented name that turned a coverage
// gate red without any defect behind it. Deriving from the SSOT means a new
// wrapper view joins the partition check the moment it is defined.
func viewChainNames(t *testing.T) []string {
	t.Helper()
	root := repoRootFromCaller(t)
	// Two sources, not one. db/request_logs_view_schema.go defines three of
	// the four live views; request_logs_bodies_with_current_month is created by
	// migration 353 and lives only in the baseline schema. Deriving from the Go
	// SSOT alone silently dropped it — and that is precisely the relation that
	// fell out of both families in §9.172's first draft, so a derivation that
	// can quietly lose a member is worse than the hand-kept list it replaced.
	sources := []string{
		filepath.Join("db", "request_logs_view_schema.go"),
		filepath.Join("sql", "schema", "01-schema.sql"),
	}
	re := regexp.MustCompile(`(?i)\bVIEW\s+(public\.)?(request_logs[a-z0-9_]*)`)
	seen := map[string]bool{}
	for _, rel := range sources {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read view schema source %s: %v — the partition check is built from it", rel, err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			seen[strings.ToLower(m[2])] = true
		}
	}
	// A schema file is not a statement of what exists. sql/schema/01-schema.sql
	// still carries request_logs_bodies_progress, which migration 573 dropped
	// and which no Go code reads; deriving from the file alone yields "every
	// name ever written into a schema file" and then reports a classification
	// hole for a relation gone for hundreds of migrations. Replaying forward
	// migrations settles it from the ordered source instead.
	live := liveRelations(t, root)
	var names []string
	for n := range seen {
		if live[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	// A floor, stated as positive facts rather than as a count. The count itself
	// is not asserted: it moves whenever a wrapper view is added, and a moving
	// count is a ratchet, not a gate. These two must be present because the
	// canonical view and the bodies view are what every reader in the inventory
	// is ultimately pointed at.
	for _, must := range []string{
		"request_logs_with_current_month",
		"request_logs_bodies_with_current_month",
	} {
		if !seen[must] {
			t.Fatalf("view chain derivation lost %q (derived %d names: %s) — the partition check "+
				"would be vacuous for the one relation family that has no base table", must, len(names),
				strings.Join(names, " "))
		}
	}
	return names
}

// v1BaseTableNames is the v1 family, read off pg_class.relkind in ('r','p') on
// the local instance: request_logs and request_logs_bodies are partitioned
// parents, request_logs_hot and request_logs_bodies_hot are plain tables.
// request_logs_archive is a fifth parent and is here because a local catalog
// query is what surfaced it — an inventory written from memory is how it would
// have been missed. Kept explicit because these are tables, not views, and
// nothing in the view schema names them as definitions.
var v1BaseTableNames = []string{
	"request_logs",
	"request_logs_archive",
	"request_logs_bodies",
	"request_logs_bodies_hot",
	"request_logs_hot",
}

// liveRelations replays forward migrations and returns which request_logs
// relations still exist, last writer winning.
//
// "Any DROP removes the name" is wrong and fails loudly here: migrations 717,
// 738 and 740 each `DROP VIEW IF EXISTS ... CASCADE` immediately before
// re-creating the same three views, so a naive drop-set empties the entire
// view chain and the floor assertion fires on a schema that is perfectly
// healthy. Last-writer-wins is the only rule that matches how these migrations
// are actually written. .down.sql files are excluded — they recreate what their
// up migration dropped, and reading them as forward history would resurrect
// exactly the retired names this replay exists to remove.
func liveRelations(t *testing.T, root string) map[string]bool {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "sql", "migrations"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".sql") && !strings.HasSuffix(p, ".down.sql") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk sql/migrations: %v — the live-relation replay reads it", err)
	}
	// filepath.WalkDir already walks in lexical order, and migration files are
	// numbered with a zero-padded prefix, so this replay is ordered correctly
	// without help. The explicit sort is kept as a cheap guard against that
	// being an implementation detail rather than a contract — but it is not
	// load-bearing, and a mutation that removes it stays green (verified). The
	// comment it replaced claimed otherwise; that claim was never tested.
	sort.Strings(files)

	live := map[string]bool{}
	drop := regexp.MustCompile(`(?i)DROP\s+(?:VIEW|TABLE)\s+(?:IF\s+EXISTS\s+)?(?:public\.)?(request_logs[a-z0-9_]*)`)
	mk := regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:VIEW|TABLE)\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?(request_logs[a-z0-9_]*)`)
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := drop.FindStringSubmatch(line); m != nil {
				live[strings.ToLower(m[1])] = false
			}
			if m := mk.FindStringSubmatch(line); m != nil {
				live[strings.ToLower(m[1])] = true
			}
		}
	}
	return live
}

// stripSQLComments removes `-- ...` to end of line and `/* ... */` from a SQL
// body. It is deliberately conservative about quoted regions: an apostrophe
// inside a string literal is common in these queries, and mangling one would
// change a classification silently. A `--` inside a quoted literal is left in
// place, which can only leave a name un-stripped, never invent a relation.
func stripSQLComments(raw string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(raw, "\n") {
		if inBlock {
			if i := strings.Index(line, "*/"); i >= 0 {
				line, inBlock = line[i+2:], false
			} else {
				continue
			}
		}
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		for {
			i := strings.Index(line, "/*")
			j := strings.Index(line, "*/")
			if i < 0 {
				break
			}
			if j > i {
				line = line[:i] + line[j+2:]
				continue
			}
			line, inBlock = line[:i], true
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func sumProxy(all []populationEntry) int {
	n := 0
	for _, c := range all {
		n += c.proxy
	}
	return n
}

func containsName(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
