// Guard for the deprecated-by-abandonment `internal/fsstore` fallback matrix
// and for the observability surface of the live DB-degradation path.
//
// Why this guard exists (2026-10-03, audit round 230):
//
// `internal/fsstore/fallback.go:3-11` carries a three-row decision matrix.
// The third row is a promise with no implementation:
//
//	//  runtime PG error → Mode = "degraded"
//
// but `Mode` has exactly two enumerated values, `ModePG = "pg"` (:48) and
// `ModeFS = "fs"` (:51), assigned only inside `NewFallback` at construction
// time. The package has no setter, no mutex/once, no background goroutine and
// no run-time switch on `Mode` — the only occurrence of the string "degraded"
// anywhere in the package is that comment line itself.
//
// That is the one condition here with teeth: if somebody ever implements the
// promised third state, the package's design has been revived, and it collides
// with the live mechanism (`domains/dbdegradation`, `DBStatusDegraded` at
// `monitor.go:150`). The guard fails on the code form and passes on the
// comment form, so it can never be satisfied by editing the documentation.
//
// The second test only prints. DB degradation is a real event that silently
// moves request logging out of the database (`telemetry/client.go:1143-1148`),
// and as of this round it has no Prometheus metric and no alert rule — only a
// `slog.Warn` (`main.go:5797`) and five registered admin endpoints. Whether to
// add the metric is decision item 97, still undecided, so a hard failure
// condition would simply be a permanently red gate (playbook §157). It prints
// the four-surface inventory instead, and says so when a gap is closed.
package sqlguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFsstoreModeHasNoDegradedVariant fails if `degraded` leaves the comment
// world inside internal/fsstore.
//
// The discriminator is AST-based on purpose: a substring search cannot tell a
// comment from a string literal from an identifier, and a guard that accepts
// "there is no degraded anywhere" would be satisfied forever by the very
// comment that documents the missing state.
func TestFsstoreModeHasNoDegradedVariant(t *testing.T) {
	root := repoRootForTest(t)
	dir := filepath.Join(root, "internal", "fsstore")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	totalComments := 0
	totalCode := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fset := token.NewFileSet()
		// ParseComments is REQUIRED: without it f.Comments is always empty, and
		// the comment half of this guard silently reports 0 hits — which looks
		// exactly like "the promise was never written down".
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}

		// Two passes, because the whole point of this guard is the DIFFERENCE
		// between the two forms:
		//
		//   - comment form: the unfulfilled promise in fallback.go:8. Comments
		//     produce no AST identifiers, so it is counted separately and never
		//     fails the gate.
		//   - code form: an identifier or string literal. Its existence means the
		//     third state was implemented and the design was revived.
		commentHits := 0
		codeHits := 0
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "degraded") {
					commentHits++
					rel, _ := filepath.Rel(root, path)
					t.Logf("%s:%d 注释形态提及 degraded（矩阵承诺，不触发失败）", rel, fset.Position(c.Pos()).Line)
				}
			}
		}

		ast.Inspect(f, func(n ast.Node) bool {
			var ident string
			switch node := n.(type) {
			case *ast.Ident:
				ident = node.Name
			case *ast.BasicLit:
				if node.Kind == token.STRING {
					ident = strings.Trim(node.Value, `"`)
				}
			}
			if !strings.EqualFold(ident, "degraded") {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s:%d 出现了代码形态的 degraded（标识符或字符串字面量）。\n"+
				"internal/fsstore 的 Mode 只有 pg/fs 两个枚举值，fallback.go:8 承诺的\n"+
				"「runtime PG error → Mode = \"degraded\"」从未实现。若现在补上，\n"+
				"该包的设计即被复活，必须先裁决它与现役 domains/dbdegradation 的\n"+
				"DBStatusDegraded（monitor.go:150）的分工，否则会出现两套运行期降级。",
				rel, fset.Position(n.Pos()).Line)
			return true
		})
		totalComments += commentHits
		totalCode += codeHits
	}
	t.Logf("internal/fsstore：degraded 在注释中出现 %d 次（fallback.go:8 的矩阵承诺），"+
		"在代码中出现 %d 次。", totalComments, totalCode)
	if totalCode == 0 {
		t.Logf("⇒ 第三态只有承诺、没有实现。")
	} else {
		t.Logf("⇒ 第三态已被实现，设计已复活：必须先裁决与现役 DBStatusDegraded 的分工。")
	}
}

// TestDegradedStateObservabilitySurface prints only; it never fails.
//
// "DB degradation has no metric and no alert rule" is decision item 97, still
// undecided. A 0-hit assertion about absence is evidence, not a verdict
// (playbook §157). This test records the four surfaces mechanically so that
// closing the gap later announces itself.
func TestDegradedStateObservabilitySurface(t *testing.T) {
	root := repoRootForTest(t)

	// Surface 1: admin read endpoints (the only machine-readable one today).
	adminRouters := grepLines(root, []string{"admin/"}, []string{"HandleFunc"})
	readers := 0
	for _, l := range adminRouters {
		if strings.Contains(l, "db-status") || strings.Contains(l, "degradation/") {
			readers++
			t.Logf("管理端读端：%s", strings.TrimSpace(l))
		}
	}

	// Surface 2: metrics. Degradation alerts that DO exist belong to other
	// mechanisms (LLM map-reduce, fp-slot routing), so only metric NAMES
	// emitted by metrics/ count here.
	metricHits := grepLines(root, []string{"metrics/"}, []string{"degrad"})
	if len(metricHits) == 0 {
		t.Logf("指标面：**无**（metrics/ 下无任何 degrad 相关指标）")
	} else {
		for _, l := range metricHits {
			t.Logf("指标面命中：%s", strings.TrimSpace(l))
		}
	}

	// Surface 3: alert rules.
	alertHits := grepLines(root, []string{"deploy/prometheus/alerts/", "deploy/prometheus/rules/"}, []string{"degrad"})
	dbAlert := 0
	for _, l := range alertHits {
		t.Logf("告警文件命中（需人工判定归属）：%s", strings.TrimSpace(l))
	}
	if dbAlert == 0 {
		t.Logf("告警面：**无针对 DB 降级的规则**；上面 %d 条命中均属其它机制"+
			"（LLM map-reduce 降级 / fp-slot routing_degradation）", len(alertHits))
	}

	// Surface 4: logs.
	logHits := grepLines(root, []string{"cmd/gateway/"}, []string{"DEGRADED MODE"})
	for _, l := range logHits {
		t.Logf("日志面：%s", strings.TrimSpace(l))
	}

	t.Logf("DB 降级可观测面清单：管理端读端 %d 个、指标 0 个、DB 告警规则 0 条、日志 %d 处。"+
		"⇒ 待裁决 97：进/出降级各加一个 gauge 并配一条告警规则。", readers, len(logHits))
}

// grepLines returns "<relpath>:<line>: <text>" for files under any of the given
// prefixes whose body contains one of the needles. It is deliberately a plain
// text scan: these are documentation-shaped facts ("does a rule file mention
// degradation"), not syntax-shaped ones, and round 228 established that a text
// scan is the wrong tool only when the claim is about a CODE FORM.
func grepLines(root string, prefixes, needles []string) []string {
	var out []string
	for _, prefix := range prefixes {
		base := filepath.Join(root, prefix)
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			lines := strings.Split(string(b), "\n")
			for i, l := range lines {
				for _, n := range needles {
					if strings.Contains(l, n) {
						out = append(out, filepath.ToSlash(rel)+":"+itoa(i+1)+": "+strings.TrimSpace(l))
						break
					}
				}
			}
			return nil
		})
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
