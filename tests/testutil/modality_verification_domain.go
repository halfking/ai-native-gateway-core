package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ModalityVerificationValueDomain derives the **read-level record modality
// domain** from the authoritative schema source: the CHECK constraint on
// model_modality_verification.modality (migration 825).
//
// Why this fact source matters (2026-10-29): the verdict gate
// (provider.modalityVerdictGateSQL) excludes models that have a *negative*
// read-level record for the requested modality. Its modality list is therefore
// bounded by **what read-level records can exist** — a modality that has no
// read-level rows cannot be excluded, no matter what the gate's list says.
//
// The lockstep gate used to compare the gate's list against a **hand-written**
// set of arm modalities. That denominator could be narrowed in the same commit
// that changes the judged behaviour ⇒ green gate, silent degradation. Anchoring
// it to the schema means the schema moving in either direction turns the gate
// red.
//
// repoRoot is the repository root (the directory holding sql/). Callers in
// first-level packages pass "..".
//
// Failure is fail-closed by design: if the constraint cannot be located or
// parsed, the function returns an error and the caller MUST fail the test
// rather than fall back to a stale hard-coded list.
func ModalityVerificationValueDomain(repoRoot string) ([]string, error) {
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

	const marker = "model_modality_verification_modality_check"
	var last string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		if strings.Contains(string(b), marker) {
			last = n
		}
	}
	if last == "" {
		return nil, fmt.Errorf("在 %s 里找不到 %s 的定义 —— 事实源读不到 ⇒ 门必须红（fail-closed），不许退回硬编码值域", dir, marker)
	}
	b, err := os.ReadFile(filepath.Join(dir, last))
	if err != nil {
		return nil, err
	}
	text := string(b)
	idx := strings.Index(text, marker)
	if idx < 0 {
		return nil, fmt.Errorf("迁移 %s 里没有 %s", last, marker)
	}
	// The constraint body is the CHECK right after the constraint name. Stop at
	// the next CONSTRAINT keyword (or a bounded window) so the literal scan
	// cannot pick up values that belong to a neighbouring constraint.
	seg := text[idx:]
	if end := strings.Index(seg, "CONSTRAINT"); end > 0 {
		seg = seg[:end]
	}
	if len(seg) > 400 {
		seg = seg[:400]
	}
	vals := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([a-z][a-z0-9_]*)'`).FindAllStringSubmatch(seg, -1) {
		vals[m[1]] = true
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("迁移 %s 的约束段里没解析出任何值字面量 —— 形态变了，派生失败", last)
	}
	out := make([]string, 0, len(vals))
	for v := range vals {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}
