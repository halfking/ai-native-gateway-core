// Package schemaobj assembles a real table's DDL from this repository's
// per-object SSOT, so a test fixture can never drift from the schema that
// migrations actually run against.
//
// # Why this exists
//
// A test that hand-writes a stand-in table tends to become *more permissive*
// than the real one, and the difference hides exactly the defects a fixture
// exists to catch. Measured case (2026-10-04): migration 825 creates
//
//	canonical_id bigint REFERENCES public.models_canonical(id) ON DELETE CASCADE
//
// and the real models_canonical has **no primary key and no unique constraint
// on id** — only UNIQUE (canonical_name). The FK therefore cannot be created:
//
//	ERROR: there is no unique constraint matching given keys for
//	       referenced table "models_canonical"  (SQLSTATE 42830)
//
// The alignment test was green for the whole session because its fixture said
// `id bigserial PRIMARY KEY` — it supplied the very key the real table lacks.
// The migration failed on every real environment while the gate passed.
//
// Two rules this package exists to enforce:
//
//  1. A fixture must copy what production **actually looks like**, not what the
//     test wishes it looked like.
//  2. When the repository already stores the DDL, **derive** the fixture from
//     that. A hand-copy is a second source of truth and it rots silently.
//
// # What it does and does not buy
//
// It removes the hand-copy, so a change to sql/objects/ flows into every
// fixture automatically. It does **not** apply the whole baseline: only the
// objects you name are read, and they must be listed in dependency order
// (table, then its sequence default, then its constraints).
//
// Paths are resolved by the caller's working directory, which for `go test
// ./pkg/` is that package's directory — pass "../sql/objects/..." from a
// top-level package.
package schemaobj

import (
	"os"
	"strings"
	"testing"
)

// Table returns the DDL that creates the named table plus the extra objects
// needed to make it behave like production, read from sql/objects/.
//
// base is the repo-root-relative path of the table's SSOT file, for example
// "../sql/objects/tables/models_canonical.sql". extras are further repo-root-
// relative object paths (sequence defaults, constraints, …) applied verbatim
// afterwards, in the order given.
//
// A missing or unreadable file calls t.Fatalf: a fixture that silently
// degrades into "less DDL than production" is the failure mode this package
// was written to prevent, so it must never resolve to a partial answer.
func Table(t *testing.T, base string, extras ...string) string {
	t.Helper()
	parts := make([]string, 0, len(extras)+1)
	for _, f := range append([]string{base}, extras...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read schema object %s: %v", f, err)
		}
		if !strings.Contains(string(b), "\n") {
			t.Fatalf("schema object %s looks empty or truncated (%d bytes) — refusing to "+
				"build a fixture out of it", f, len(b))
		}
		parts = append(parts, string(b))
	}
	return strings.Join(parts, "\n")
}
