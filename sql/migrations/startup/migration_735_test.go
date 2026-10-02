package startup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Migration 735 (R51 audit round, 2026-09-21) closes R50 F19: the expression
// UNIQUE index uq_models_canonical_active_folded_name on models_canonical,
// backstopping modelname.DedupCanonicalNameSQL's run-collapse arm in the
// concurrent createModel window (callers map 23505 → 409).
//
// The contract below pins the same invariants the R50 lesson demands:
//
//	C1  The index expression is byte-equivalent to the fold chain synthesized
//	    from modelname/canonical_dedup.go's foldedNameExpr (single hand-written
//	    point, per the R50 collapse lesson) wrapped in the run-collapse arm —
//	    bound by PARSING the Go source, not by copy-paste.
//	C2  A fail-closed precheck RAISEs when active fold-duplicates remain, and
//	    names the cleanup script, before the CREATE UNIQUE INDEX. (Unlike 726's
//	    ctid dedup this migration must NOT self-heal: models_canonical carries
//	    ON DELETE CASCADE FK families (migration 342), so winner selection is an
//	    ops decision reserved to
//	    sql/fixes/2026-09-20-canonical-dedup-cleanup.sql.)
//	C3  The index is created IF NOT EXISTS and the .down file drops exactly
//	    that index and nothing else.
//	C4  The installer embeddata copy stays byte-identical to the canonical file
//	    (same sync convention 721/726 documented).
func TestMigration735CanonicalFoldedUniqueContract(t *testing.T) {
	upBytes, err := os.ReadFile("735_models_canonical_active_folded_unique.sql")
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := os.ReadFile("735_models_canonical_active_folded_unique.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	down := string(downBytes)

	// ── C1: index expression == foldedNameExpr chain + run-collapse arm ──
	goSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "modelname", "canonical_dedup.go"))
	if err != nil {
		t.Fatal(err)
	}
	goStr := string(goSrc)
	// Extract foldedNameExpr's Sprintf format string (the single hand-written
	// fold chain) and synthesize the run-collapse expression from it.
	m := regexp.MustCompile(`(?s)func foldedNameExpr\(target string\) string \{\s*return fmt\.Sprintf\(\s*"(.*?)"`).FindStringSubmatch(goStr)
	if m == nil {
		t.Fatalf("foldedNameExpr Sprintf template not found in modelname/canonical_dedup.go")
	}
	foldChain := fmt.Sprintf(m[1], "canonical_name")
	if !strings.Contains(foldChain, "lower(canonical_name)") {
		t.Fatalf("synthesized fold chain unexpected: %q", foldChain)
	}
	wantExpr := fmt.Sprintf("regexp_replace(%s, '[-_]{2,}', '_', 'g')", foldChain)

	idxAt := strings.Index(up, "CREATE UNIQUE INDEX IF NOT EXISTS uq_models_canonical_active_folded_name")
	if idxAt < 0 {
		t.Fatalf("CREATE UNIQUE INDEX uq_models_canonical_active_folded_name not found")
	}
	if idxAt != strings.LastIndex(up, "CREATE UNIQUE INDEX") {
		t.Fatalf("more than one CREATE UNIQUE INDEX statement")
	}
	// Normalize whitespace only; the expression itself must appear verbatim.
	norm := func(s string) string {
		return regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	}
	if !strings.Contains(norm(up), norm(wantExpr)) {
		t.Fatalf("index expression drift:\n want ~%q\n in: %q", wantExpr, norm(up))
	}
	if !strings.Contains(up[idxAt:], "WHERE status = 'active'") {
		t.Fatalf("index must be partial on status = 'active'")
	}

	// ── C2: fail-closed precheck before the index ──────────────────────
	pre := up[:idxAt]
	if !strings.Contains(pre, "RAISE EXCEPTION") {
		t.Fatalf("precheck must fail closed with RAISE EXCEPTION before CREATE UNIQUE INDEX")
	}
	if !strings.Contains(pre, "fold-duplicate") || !strings.Contains(pre, "2026-09-20-canonical-dedup-cleanup.sql") {
		t.Fatalf("precheck must name the fold-duplicate condition and the cleanup script")
	}

	// ── C3: idempotent up, surgical down ───────────────────────────────
	if !strings.Contains(up, "IF NOT EXISTS") {
		t.Fatalf("CREATE UNIQUE INDEX must be IF NOT EXISTS (re-run no-op)")
	}
	downCode := regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(down, "")
	downCode = strings.TrimSpace(downCode)
	if downCode != "DROP INDEX IF EXISTS uq_models_canonical_active_folded_name;" {
		t.Fatalf(".down must drop exactly the 735 index, got: %q", downCode)
	}

	// ── C4: installer embeddata copy byte-identical ────────────────────
	for _, name := range []string{
		"735_models_canonical_active_folded_unique.sql",
		"735_models_canonical_active_folded_unique.down.sql",
	} {
		emb, err := os.ReadFile(filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if err != nil {
			t.Fatalf("embeddata copy missing for %s: %v", name, err)
		}
		orig, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(orig, emb) {
			t.Fatalf("embeddata copy of %s drifted from canonical source (embeddata 同步 convention)", name)
		}
	}
}
