// Package testutil hosts shared test fakes and shared test-only derivations
// for the llm-gateway-go test suites.
//
// The fakes implement the public interfaces exported by
// domains/streaming/state (PreStream / StreamWriter / Compressor) and
// are intentionally placed in tests/testutil rather than inside the
// production package so the state-machine code stays free of test-only
// symbols. They are *not* for production use.
//
// CanonicalModalityValueDomain lives here for the same reason: it is a
// **test-only derivation** of a schema fact, and two packages need the same
// derivation. Keeping one implementation here is deliberate — a second copy
// in each package would be two口径 that can drift (§411: 同一口径只能有一份
// 实现).
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// resolveMigrationsDir finds sql/migrations/startup by walking up from the
// given root.
//
// Why walk up instead of trusting the caller: the test working directory is
// the package directory, so the repository root is ".." for a first-level
// package (provider) but "../.." for a second-level one (domains/streaming).
// Making the callers compute that difference is a silent-failure generator
// (the wrong root fails with "no such file or directory" — which reads like a
// missing schema, not like a wrong argument). Walking up removes the
// difference entirely.
func resolveMigrationsDir(root string) (string, error) {
	cur, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for i := 0; i < 6; i++ {
		cand := filepath.Join(cur, "sql", "migrations", "startup")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return "", fmt.Errorf("从 %s 向上找不到 sql/migrations/startup —— 事实源读不到 ⇒ 门必须红（fail-closed），不许退回硬编码值域", root)
}

// CanonicalModalityValueDomain derives the **registered modality value domain**
// from the authoritative schema source: the CHECK constraint on
// models_canonical.modality (migration 451 and any later revision of it).
//
// The returned slice always contains "" (the empty registration, which SQL
// normalises to "text" via COALESCE) plus every literal the CHECK allows.
//
// Why a derivation instead of a hard-coded list: the value domain is the
// **denominator** of the modality lockstep gates. A hard-coded denominator can
// be narrowed in the same commit that changes the judged behaviour, and the
// gate then reports zero drift while production silently degrades (measured:
// dropping "video" from the domain *and* from the SQL arm ⇒ zero drift ⇒
// gate green). Anchoring the denominator to the schema means the schema
// moving in either direction turns the gate red.
//
// repoRoot is the repository root (the directory holding sql/). Callers in
// first-level packages pass "..".
//
// Failure is fail-closed by design: if the constraint cannot be located or
// parsed, the function returns an error and the caller MUST fail the test
// rather than fall back to a stale hard-coded list.
func CanonicalModalityValueDomain(repoRoot string) ([]string, error) {
	dir, err := resolveMigrationsDir(repoRoot)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读迁移目录 %s 失败：%w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	// Sort by file name (not mtime): mtime is rewritten by checkouts and copies,
	// and the migration numbering is the authoritative order.
	sort.Strings(names)

	const marker = "models_canonical_modality_check"
	var last string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		text := string(b)
		if !strings.Contains(text, marker) {
			continue
		}
		// Only count migrations that ADD the constraint — a later migration that
		// merely DROPs it (a rollback shape) must not become the source.
		if !strings.Contains(text, "ADD CONSTRAINT") {
			continue
		}
		last = n
	}
	if last == "" {
		return nil, fmt.Errorf("在 %s 里找不到定义 %s 的迁移 —— 事实源读不到 ⇒ 门必须红（fail-closed），不许退回硬编码值域", dir, marker)
	}
	b, err := os.ReadFile(filepath.Join(dir, last))
	if err != nil {
		return nil, err
	}
	text := string(b)
	idx := strings.LastIndex(text, "ADD CONSTRAINT")
	if idx < 0 {
		return nil, fmt.Errorf("迁移 %s 里没有 ADD CONSTRAINT", last)
	}
	seg := text[idx:]
	vals := map[string]bool{}
	// The CHECK uses either ARRAY[...] or IN (...) — both are quoted literals.
	for _, m := range regexp.MustCompile(`'([a-z][a-z0-9_]*)'`).FindAllStringSubmatch(seg, -1) {
		vals[m[1]] = true
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("迁移 %s 的约束段里没解析出任何值字面量 —— 形态变了，派生失败", last)
	}
	out := []string{""}
	for v := range vals {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}
