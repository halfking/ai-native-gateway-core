package startup

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Migration 726 (2026-09-18 154 生产事故) restores the unique index on
// credential_model_index_hot (bucket, credential_id, raw_model) that migration
// 718's "keep idx_credential_model_index_hot_unique" assumption silently
// removed on environments where that index never existed (SQLSTATE 42P10 on
// every auto route rollup since deploy 2138).
//
// The contract below pins four invariants so this class of drift cannot
// silently recur:
//
//	C1  726 up creates the canonical unique index on EXACTLY the tuple the
//	    Go rollup's ON CONFLICT clause resolves against (bound by parsing
//	    bg/auto_index_refresher.go, not by copy-paste).
//	C2  726 up defensively dedupes (ctid guard) before CREATE UNIQUE INDEX,
//	    so environments that accumulated duplicate rows inside the
//	    constraint-less window converge instead of failing the migration.
//	C3  No startup migration ever DROPs the canonical index name — 718 is
//	    allowed to drop the two redundant siblings only. (726.down is the
//	    one legitimate dropper; .down files are excluded from the scan.)
//	C4  The installer embeddata copies stay byte-identical to the originals
//	    (same sync convention migration 721 documented as "embeddata 同步").
func TestMigration726RestoreHotUniqueContract(t *testing.T) {
	upBytes, err := os.ReadFile("726_restore_credential_model_index_hot_unique.sql")
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := os.ReadFile("726_restore_credential_model_index_hot_unique.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	down := string(downBytes)

	const (
		canonicalIndex = "idx_credential_model_index_hot_unique"
		conflictTuple  = "(BUCKET, CREDENTIAL_ID, RAW_MODEL)" // normalized form; see normalizeSQL
	)

	// ── C1: canonical unique index on the rollup conflict tuple ────────
	upCompact := normalizeSQL(up)
	createClause := "CREATE UNIQUE INDEX IF NOT EXISTS " + strings.ToUpper(canonicalIndex)
	if !strings.Contains(upCompact, createClause) {
		t.Errorf("726 up must CREATE UNIQUE INDEX IF NOT EXISTS %s (idempotent on fresh installs where 354 already created it)", canonicalIndex)
	}
	idxTuple := indexTupleFromMigration(upCompact, canonicalIndex)
	if idxTuple != conflictTuple {
		t.Errorf("726 index tuple = %s, want %s (must match the rollup ON CONFLICT key)", idxTuple, conflictTuple)
	}

	// Bind the tuple to the Go rollup source instead of trusting a
	// copy-pasted literal: parse every `ON CONFLICT (...)` in
	// bg/auto_index_refresher.go and require the canonical tuple to be one
	// of them (the other known member is model_task_index's
	// (bucket, canonical_id, task_type)).
	bgSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "bg", "auto_index_refresher.go"))
	if err != nil {
		t.Fatal(err)
	}
	onConflictTuples := onConflictTuplesFromGoSource(string(bgSrc))
	if len(onConflictTuples) == 0 {
		t.Fatal("no ON CONFLICT clause found in bg/auto_index_refresher.go — the rollup SQL shape changed; re-audit migration 726 against it")
	}
	found := false
	for _, tuple := range onConflictTuples {
		if tuple == conflictTuple {
			found = true
		}
	}
	if !found {
		t.Errorf("rollup ON CONFLICT tuples %v no longer contain %s — update migration 726 + this test together", onConflictTuples, conflictTuple)
	}

	// ── C2: dedupe guard before CREATE UNIQUE INDEX ────────────────────
	deletePos := strings.Index(upCompact, "DELETE FROM CREDENTIAL_MODEL_INDEX_HOT A")
	createPos := strings.Index(upCompact, createClause)
	if deletePos < 0 || createPos < 0 || deletePos > createPos {
		t.Errorf("726 up must run the defensive dedupe DELETE before CREATE UNIQUE INDEX (ctid-guarded convergence for the constraint-less window)")
	}
	for _, frag := range []string{"USING CREDENTIAL_MODEL_INDEX_HOT B", "A.CTID", "> B.CTID"} {
		if !strings.Contains(upCompact, frag) {
			t.Errorf("726 dedupe guard lost fragment %q", frag)
		}
	}
	if !strings.Contains(upCompact, "BEGIN;") || !strings.Contains(upCompact, "COMMIT;") {
		t.Errorf("726 up must be transactional (dedupe + DDL atomic), got BEGIN/COMMIT=%v/%v",
			strings.Contains(upCompact, "BEGIN;"), strings.Contains(upCompact, "COMMIT;"))
	}

	// ── C3: no startup migration may drop the canonical index ──────────
	upMatcher := regexp.MustCompile(`(?i)DROP\s+INDEX\s+(CONCURRENTLY\s+)?IF\s+EXISTS\s+` + canonicalIndex + `\b`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var droppers []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue // .down files own the legitimate reversal
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if upMatcher.Match(body) {
			droppers = append(droppers, name)
		}
	}
	if len(droppers) > 0 {
		t.Errorf("startup migration(s) %v DROP %s — that is the 718-class mistake this contract guards (the surviving-side index must never be dropped)", droppers, canonicalIndex)
	}

	// ── down: reversal limited to the canonical index ───────────────────
	downCompact := normalizeSQL(down)
	if got := strings.Count(downCompact, "DROP INDEX IF EXISTS"); got != 1 {
		t.Errorf("726 down must drop exactly 1 index, found %d", got)
	}
	if !strings.Contains(downCompact, "DROP INDEX IF EXISTS "+strings.ToUpper(canonicalIndex)) {
		t.Errorf("726 down must target %s only", canonicalIndex)
	}

	// ── C4: installer embeddata byte-identical ─────────────────────────
	embedRoot := filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup")
	for _, pair := range []struct{ orig, embed string }{
		{"726_restore_credential_model_index_hot_unique.sql", filepath.Join(embedRoot, "726_restore_credential_model_index_hot_unique.sql")},
		{"726_restore_credential_model_index_hot_unique.down.sql", filepath.Join(embedRoot, "726_restore_credential_model_index_hot_unique.down.sql")},
	} {
		embedBytes, err := os.ReadFile(pair.embed)
		if err != nil {
			t.Fatalf("embeddata copy missing (%s): %v", pair.embed, err)
		}
		origBytes, err := os.ReadFile(pair.orig)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(origBytes, embedBytes) {
			t.Errorf("embeddata copy drifted from original: %s", pair.embed)
		}
	}
}

// normalizeSQL uppercases and collapses all whitespace runs so tuple and
// clause matching survives formatting edits.
func normalizeSQL(s string) string {
	return strings.Join(strings.Fields(strings.ToUpper(s)), " ")
}

// indexTupleFromMigration returns the column tuple the given index is created
// on, normalized to "(COL, COL, COL)" form. upCompact is expected to be
// normalizeSQL output (uppercased, whitespace-collapsed); indexName may be
// any case.
func indexTupleFromMigration(upCompact, indexName string) string {
	re := regexp.MustCompile(`CREATE UNIQUE INDEX IF NOT EXISTS ` + regexp.QuoteMeta(strings.ToUpper(indexName)) + ` ON \S+ \(([^)]*)\)`)
	m := re.FindStringSubmatch(upCompact)
	if m == nil {
		return ""
	}
	cols := strings.Split(m[1], ",")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	sort.Strings(cols)
	return "(" + strings.Join(cols, ", ") + ")"
}

// onConflictTuplesFromGoSource extracts normalized `ON CONFLICT (...)` tuples
// from the rollup Go source.
func onConflictTuplesFromGoSource(src string) []string {
	re := regexp.MustCompile(`(?i)ON\s+CONFLICT\s*\(([^)]*)\)`)
	matches := re.FindAllStringSubmatch(src, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		cols := strings.Split(m[1], ",")
		for i := range cols {
			cols[i] = strings.ToUpper(strings.TrimSpace(cols[i]))
		}
		sort.Strings(cols)
		tuple := "(" + strings.Join(cols, ", ") + ")"
		if !seen[tuple] {
			seen[tuple] = true
			out = append(out, tuple)
		}
	}
	return out
}
