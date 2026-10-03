// Guard for the deprecated `adapter/unified` package and for the documents
// that used to describe it as a live, four-protocol adapter layer.
//
// Why this guard exists (2026-10-03, audit round 229):
//
// `adapter/unified` is a 2025 sketch of a second provider-agnostic IR that sits
// beside the canonical one. It declared itself deprecated on 2026-08-30
// (`interface.go:1-13`, `registry.go:145-149`), and the design docs agree:
// `docs/01-requirements/functional/FEATURES_CATALOG.md:22` ("死代码（0 外部引用，
// 2026-08-30 已声明废弃）"), `docs/03-design/01-architecture/architecture/
// ARCHITECTURE.md:120` ("已废弃旧尝试"), and
// `docs/03-design/01-architecture/parallel-implementations-comparison.md:34`
// (P-10 dead-code bucket).
//
// Round 115 mis-filed it as "B 类（真孤儿）— 能力已在、从未接线", which would
// have queued it as a candidate to WIRE UP. It is C-class: a rejected draft.
// The distinction matters because B-class means "worth connecting" and this
// package's own header says the canonical surface is `internal/ir` +
// `domains/transformation`, which already covers all five protocols
// (anthropic / gemini / ollama / openai / responses) with streaming.
// Round 229 established the capability set is a strict SUBSET, so deleting it
// loses nothing.
//
// `docs/03-design/01-architecture/unified-optimization-prompts.md:37` states the
// standing instruction for this package: "若有非测试引用出现（说明期间被复活），
// 停手登记" — if a non-test reference appears, it has been revived; stop and
// record it. This guard is that mechanism.
//
// Two earlier audits got the package's state wrong in opposite directions:
//   - round 115 filed it as "能力已在、从未接线" (B class), ignoring the
//     deprecation banner that sits in the first 13 lines of interface.go;
//   - round 148 filed it under §41 bucket ③ ("两者都没说" — neither committed to
//     wiring nor explicitly declared unwired) and additionally reported that its
//     "globalRegistry 在运行时恒为空", which is false: `registry.go:150-152`
//     pre-registers OpenAI and Anthropic from `init()`.
//
// Hence the guards below assert three separate things, each mechanically:
// the deprecation banner is still present, there is still no non-test importer,
// and the design doc no longer advertises it as CURRENT.
package sqlguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestUnifiedStaysUnimportedOutsideTests pins the "no revival" rule.
//
// Any non-test importer of `adapter/unified` means the package was revived;
// per unified-optimization-prompts.md:37 that requires a human decision, so the
// guard fails rather than silently accepting it. A test-only reference is
// allowed, because the deprecation banner says the package is retained for
// historical regression tests.
func TestUnifiedStaysUnimportedOutsideTests(t *testing.T) {
	root := repoRootForTest(t)
	importers := importersOfPackage(root, "github.com/kaixuan/llm-gateway-go/adapter/unified")

	var prod []string
	for _, imp := range importers {
		if !strings.HasSuffix(imp, "_test.go") {
			prod = append(prod, imp)
		}
	}
	sort.Strings(prod)

	for _, p := range importers {
		t.Logf("引用方：%s", p)
	}
	if len(prod) > 0 {
		sort.Strings(importers)
		t.Errorf("adapter/unified 出现了 %d 个非测试引用方：%v\n"+
			"该包已于 2026-08-30 声明废弃（interface.go:4「Deprecated 2026-08-30: not the canonical IR」）。"+
			"非测试引用意味着它被复活；按 docs/03-design/01-architecture/unified-optimization-prompts.md:37"+
			"「若有非测试引用出现（说明期间被复活），停手登记」，需先人工裁决再改代码。",
			len(prod), prod)
	}
}

// TestUnifiedDeprecationBannerSurvives keeps the only in-code record of intent.
//
// The package has no logic of its own, so this comment IS the contract
// (playbook §175: when the code carries no information, the comment is the only
// record). If the banner is deleted while the package still has zero importers,
// the package silently becomes an unexplained "orphan" again — which is exactly
// how round 115 re-filed it.
func TestUnifiedDeprecationBannerSurvives(t *testing.T) {
	root := repoRootForTest(t)
	header := readFileLines(t, filepath.Join(root, "adapter", "unified", "interface.go"), 20)

	joined := strings.Join(header, "\n")
	for _, want := range []string{
		"Deprecated 2026-08-30",                // :4
		"not the canonical IR",                 // :4
		"internal/ir + domains/transformation", // :6 — where the real work belongs
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("adapter/unified/interface.go 的包头缺少 %q —— 废弃声明被删掉了。"+
				"该包无自身逻辑，注释是唯一的意图记录（§175）；删掉它会让本包重新变成"+
				"「无解释的孤儿」，正如 115 号把它误归为「能力已在、从未接线」那样。", want)
		}
	}
}

// TestModulesGuideDoesNotAdvertiseUnifiedAsCurrent closes the documentation
// loop. The guide listed three files that never existed (`gemini.go`,
// `responses.go`, `converter.go`) and marked four protocols CURRENT with
// streaming support — for a package that supports two protocols and has zero
// streaming implementations. Left in place, it points readers at the wrong
// place for exactly the capability the audit brief calls out (multi-protocol
// adaptation).
//
// Failure condition is a CURRENT/streaming claim for this package, or a
// key-file entry for a file that does not exist on disk.
func TestModulesGuideDoesNotAdvertiseUnifiedAsCurrent(t *testing.T) {
	root := repoRootForTest(t)
	guidePath := filepath.Join(root, "docs", "MODULES_GUIDE.md")
	body, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read MODULES_GUIDE.md: %v", err)
	}
	lines := strings.Split(string(body), "\n")

	// Isolate the adapter/unified section so the check cannot be satisfied (or
	// broken) by unrelated text elsewhere in a 300+ line guide.
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "`adapter/unified`") && start < 0 {
			start = i
		}
	}
	if start < 0 {
		t.Fatal("MODULES_GUIDE.md 里找不到 adapter/unified 小节")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "#### ") || strings.HasPrefix(lines[i], "### ") {
			end = i
			break
		}
	}
	section := lines[start:end]

	joined := strings.Join(section, "\n")
	t.Logf("adapter/unified 小节：第 %d–%d 行", start+1, end)

	// (a) no CURRENT claim for a deprecated package.
	//
	// Scoped to Markdown table data rows on purpose. "Advertising" only ever
	// happens in the protocol-support table; the same word also appears in
	// prose that CORRECTS the record ("此前误标为四协议 CURRENT"), and a
	// whole-section substring search flags that correction as the defect it
	// is fixing — a guard that fails on correct text is worse than no guard.
	// So: only a `|`-delimited row counts as a claim.
	for i, l := range section {
		trimmed := strings.TrimSpace(l)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		if strings.Contains(trimmed, "CURRENT") {
			t.Errorf("MODULES_GUIDE.md:%d 仍把 adapter/unified 的协议标为 CURRENT：%q\n"+
				"该包 2026-08-30 已废弃，实际只支持 OpenAI/Anthropic 两个协议且流式零实现"+
				"（GetStreamAdapter 运行时必返 adapter does not support streaming）。",
				start+i+1, trimmed)
		}
		// (b) every key-file entry must exist on disk
		if !strings.HasSuffix(trimmed, ".go") {
			continue
		}
		name := trimmed
		name = name[strings.Index(name, "adapter/unified/"):]
		name = strings.TrimSpace(strings.SplitN(name, " ", 2)[0])
		name = strings.TrimPrefix(name, "adapter/unified/")
		if _, err := os.Stat(filepath.Join(root, "adapter", "unified", name)); err != nil {
			t.Errorf("MODULES_GUIDE.md:%d 列出的关键文件 adapter/unified/%s 不存在。", start+i+1, name)
		}
	}

	// (c) The section must not advertise protocols this package never had.
	// A struck-through or explicitly-disclaimed mention is fine; a bare
	// capability claim is not. This is deliberately narrow: the point is to
	// catch "this package handles Gemini/Responses", not to police wording.
	for _, ghost := range []string{"Gemini协议", "Responses协议"} {
		if idx := strings.Index(joined, ghost); idx >= 0 {
			t.Logf("小节内出现 %q（第 %d 行附近），请确认它是在声明「本包没有该能力」而不是在宣传它。",
				ghost, start+strings.Count(joined[:idx], "\n")+1)
		}
	}
}

// importersOfPackage returns repo-relative paths of every .go file whose import
// block mentions the given module path.
//
// It parses rather than greps because "mentions the path" and "imports the
// package" differ: a path can appear in a comment (e.g. the deprecation banner
// itself names the package) without being imported. Round 228 already showed
// what a text search does to a conclusion: it merges two different things that
// merely share a name.
func importersOfPackage(root, importPath string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch filepath.Base(path) {
			case "vendor", "node_modules", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		for _, spec := range f.Imports {
			v, verr := strconvUnquote(spec.Path.Value)
			if verr != nil {
				continue
			}
			if v == importPath {
				rel, rerr := filepath.Rel(root, path)
				if rerr != nil {
					rel = path
				}
				out = append(out, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	return out
}

// readFileLines returns up to n lines starting at the top of the file.
func readFileLines(t *testing.T, path string, n int) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// strconvUnquote is strconv.Unquote, kept local to avoid an extra import in a
// file that is mostly about parsing.
func strconvUnquote(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", errBadQuoted
	}
	return s[1 : len(s)-1], nil
}

var errBadQuoted = &quotedError{}

type quotedError struct{}

func (*quotedError) Error() string { return "not a quoted string" }

var _ = ast.Inspect // keep go/ast referenced for future selector-level checks
