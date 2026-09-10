package discovery

// canonical_maintain_test.go — regression coverage for the matched-standard
// fast path's models_canonical maintenance (2026-09-11). Before the fix,
// upsertModel / EnsureCanonicalAndAliases returned right after picking the
// matched row, so the INSERT ... ON CONFLICT branch's three maintenance
// rules (family:<id> tag backfill, split-family normalization, sticky-
// modality upgrade) never ran for matched rows.

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// maintUpdateSQL pins the guarded-UPDATE shape: it must target the matched
// row by id ($1) and admit it only via one of the three maintenance
// conditions, so healthy rows stay write-free on every discovery pass.
const maintUpdateSQL = `(?s)UPDATE models_canonical\s+SET family = CASE` +
	`.*WHERE id = \$1` +
	`.*\(family = \$2 AND NOT tags @> ARRAY\['family:' \|\| \$2\]\)` +
	`.*OR family = ANY\(\$3::text\[\]\)` +
	`.*OR \(modality = 'text' AND \$4 <> 'text'\)`

// The helper must derive $2 from the canonical name via InferFamily and $4
// from the RAW provider name via InferModality — using the raw (not the
// canonical) name is what lets a matched row upgrade its modality when the
// provider feed's spelling carries a richer signal than the row's stale
// 'text' default.
func TestMaintainMatchedCanonical_GuardedUpdateShape(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	const canonicalID = 42
	// "gpt-4o" → family openai-gpt (vendorCanonicalFamilies collapse) and a
	// non-text inferred modality, so the test fails if either derivation
	// is dropped or reordered.
	rawName := "gpt-4o"
	wantModality := modelname.InferModality(rawName)
	if wantModality == "text" {
		t.Fatalf("sanity: InferModality(%q) = text, test expects a richer value", rawName)
	}

	mock.ExpectExec(maintUpdateSQL).
		WithArgs(canonicalID, "openai-gpt", splitFamilyIDs, wantModality).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := maintainMatchedCanonical(context.Background(), mock, canonicalID, "gpt-4o", rawName); err != nil {
		t.Fatalf("maintainMatchedCanonical: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintainMatchedCanonical_DBErrorPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectExec(`(?s)UPDATE models_canonical.*WHERE id = \$1`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("connection reset"))

	if err := maintainMatchedCanonical(context.Background(), mock, 7, "gpt-4o", "gpt-4o"); err == nil {
		t.Fatal("expected the DB error to propagate so callers can warn")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// End-to-end through the exported refresh path: when the raw name matches an
// existing standard model, EnsureCanonicalAndAliases must run the maintenance
// UPDATE between the catalog read and the alias seeding. This is the same
// helper upsertModel's matched branch calls (that branch reads s.db, a
// concrete *pgxpool.Pool, so it is covered indirectly via this shared seam).
func TestEnsureCanonicalAndAliases_MatchedPathRunsMaintenance(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	rawName := "cluade/opus-5"
	mock.ExpectQuery(`FROM models_canonical`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "canonical_name"}).AddRow(42, "claude-opus-5"))

	mock.ExpectExec(`(?s)UPDATE models_canonical.*WHERE id = \$1`).
		WithArgs(42, "anthropic-claude", splitFamilyIDs, modelname.InferModality(rawName)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// seedCanonicalAliases: one INSERT per deduped, non-empty normalized
	// alias. Mirror the loop to size the expectation list.
	seen := map[string]struct{}{}
	for _, alias := range GenerateAliases(rawName, "claude-opus-5") {
		normalized := modelname.CanonicalizeClientModel(alias)
		if normalized == "" {
			continue
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		mock.ExpectExec(`INSERT INTO model_aliases`).
			WithArgs(42, pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}

	id, name, err := EnsureCanonicalAndAliases(context.Background(), mock, rawName, "discovery")
	if err != nil {
		t.Fatalf("EnsureCanonicalAndAliases: %v", err)
	}
	if id != 42 || name != "claude-opus-5" {
		t.Fatalf("got (%d, %q), want (42, claude-opus-5)", id, name)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// splitFamilyIDs is passed positionally as $3 and evaluated with
// = ANY(...), so only the CONTENT must track vendorCanonicalFamilies —
// but silently drifting from the map (e.g. a new split token added to
// the map without the slice picking it up) would stop normalizing that
// token in both the ON CONFLICT branch and the matched-row maintenance.
func TestSplitFamilyIDs_TrackVendorCanonicalFamilies(t *testing.T) {
	if len(splitFamilyIDs) != len(vendorCanonicalFamilies) {
		t.Fatalf("len(splitFamilyIDs)=%d, want %d", len(splitFamilyIDs), len(vendorCanonicalFamilies))
	}
	set := make(map[string]bool, len(splitFamilyIDs))
	for _, id := range splitFamilyIDs {
		set[id] = true
	}
	for token := range vendorCanonicalFamilies {
		if !set[token] {
			t.Errorf("splitFamilyIDs missing vendorCanonicalFamilies key %q", token)
		}
	}
}
