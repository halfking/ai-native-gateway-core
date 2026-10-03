package telemetry

import (
	"os"
	"regexp"
	"path/filepath"
	"strings"
	"testing"
)

// Gates for the migration-820 landing pad (audit §9.92). These replaced the
// seven request_abandoned gates that guarded migration 819, which the user
// overturned on 2026-10-03 in favour of "add a state class inside the session
// family".
//
// The three invariants worth pinning, in descending order of how quietly they
// break:
//
//  1. The predicate's DIRECTION. `markAbandonedTurn` is called from the
//     upsert-race branch, and the call is guarded by `exists == false` at the
//     call site. Inverted, it flags ~100% of normal traffic as abandoned and
//     no threshold alert can catch that (the ratio alert is calibrated against
//     a 0.047% baseline). This is the "silently says the opposite" shape.
//
//  2. BOTH session_turns faces get the column. A single-face migration is
//     "green but broken": the UPDATE silently matches 0 rows on the other
//     face, and 0 rows is indistinguishable from "nothing was abandoned".
//
//  3. No t0 placeholder row. session_turns is idempotent on
//     (tenant_id, request_id, partition_date) with ON CONFLICT DO NOTHING, so
//     inserting a placeholder would permanently pin success=false. Reverting
//     to the placeholder shape is a one-line change that would look fine.

// repoRoot is the repository root **relative to this package's directory**.
//
// ⚠ It is 4 levels up, not 3, and that is not a typo: this package has its
// own go.mod (domains/hooks/observability/telemetry/go.mod), so a "count the
// directories" walk stops one level short and every repo-file read fails with
// a path error that reads like a missing file rather than a wrong depth.
// Verified with a probe over os.path, not by counting.
const repoRoot = "../../../../"

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestAbandonedTurnPredicateIsGuardedByExistsFalse pins invariant 1: the
// fallback branch must only mark when the row is genuinely absent.
//
// This is a source-shape gate on purpose. The alternative — a pgxmock test
// asserting the SQL — would pass even if the caller passed the wrong boolean,
// because the guard lives in the *caller's* branch, not in the SQL text.
func TestAbandonedTurnPredicateIsGuardedByExistsFalse(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	s := string(src)

	// The branch must be structurally paired: RowsAffected()==0 → EXISTS probe
	// → `if exists { return nil }` → mark → INSERT fallback.
	anchor := "markAbandonedTurn(ctx, tx, entry)"
	i := strings.Index(s, anchor)
	if i < 0 {
		t.Fatalf("markAbandonedTurn is never called from client.go — the landing pad " +
			"is not wired at all (§9.92 invariant 1)")
	}

	// Everything between the EXISTS probe and the mark call must contain the
	// positive-return guard, and must NOT contain a `if !exists` style arm.
	probe := strings.Index(s, "SELECT EXISTS (")
	if probe < 0 || probe > i {
		t.Fatalf("could not locate the EXISTS probe before the mark call " +
			"(probe=%d, mark=%d) — the predicate's shape changed; re-read the " +
			"branch before trusting this gate", probe, i)
	}
	seg := s[probe:i]
	if !strings.Contains(seg, "if exists {") {
		t.Fatalf("the branch between the EXISTS probe and markAbandonedTurn does not "+
			"contain the positive `if exists { return nil }` guard.\n"+
			"Without it, markAbandonedPending would be reached for requests whose t0 "+
			"DID land — i.e. it would flag essentially all normal traffic as abandoned.\n"+
			"segment was:\n%s", seg)
	}
	// The mark call must be OUTSIDE the `if exists { … }` block, at the same
	// brace depth, and after the `return nil` that guard contains.
	//
	// ⚠ Position comparison alone is NOT enough, and the first draft of this
	// gate got that wrong: moving the mark call INSIDE `if exists { … }` still
	// leaves it after the guard textually, so `guard < i` stays true and the
	// gate passes. That mutation — "flag every request whose t0 landed", i.e.
	// ~100% false positives — passed a gate written specifically to stop it.
	// ⇒ Compare **brace depth**: the mark call must sit at the depth it had
	// before the `if exists {` opened.
	guardIdx := strings.Index(s, "if exists {")
	if guardIdx < 0 {
		t.Fatalf("no `if exists {` guard found before markAbandonedTurn — the " +
			"upsert-race branch no longer skips the row that does exist")
	}
	if !strings.Contains(s[guardIdx:i], "return nil") {
		t.Fatalf("the `if exists` guard no longer returns early — requests whose t0 " +
			"DID land would fall through to the landing pad and be flagged abandoned")
	}

	depthAtGuard := braceDepth(s[:guardIdx])
	depthAtMark := braceDepth(s[:i])
	if depthAtMark != depthAtGuard {
		t.Fatalf("markAbandonedTurn sits at brace depth %d but the `if exists` guard "+
			"opens at %d — the call is INSIDE the guard, so every request whose t0 "+
			"landed gets flagged as abandoned (depthMark=%d, depthGuard=%d)",
			depthAtMark, depthAtGuard, depthAtMark, depthAtGuard)
	}
}

// TestAbandonedTurnMarkTouchesBothTurnsFaces pins invariant 2.
func TestAbandonedTurnMarkTouchesBothTurnsFaces(t *testing.T) {
	// ⚠ Read the **comment-stripped** source. abandoned_turn.go's file header
	// documents the 819 design it replaces and names `public.session_turns` many
	// times in prose; a raw substring check therefore passes on a file that
	// writes only one face. That is the same "a comment mentioning a key is not
	// the key being written" trap the Go gates in this repo avoid via
	// stripGoCommentsKeepLines — and it is exactly what made the first draft of
	// this gate pass a mutation that removed one of the two faces.
	code := stripGoComments(string(readRepoFileBytes(t,
		"domains/hooks/observability/telemetry/abandoned_turn.go")))

	// The write must be a loop over BOTH faces. Checking for two substrings is
	// weaker than it looks: a single hard-coded table plus a comment mentioning
	// the other face satisfies it.
	if !strings.Contains(code, "for _, tbl := range") {
		t.Fatalf("abandoned_turn.go does not iterate over the turn faces — the landing " +
			"pad must write both, because a single-face write silently matches 0 rows " +
			"on the other, and 0 rows is indistinguishable from \"nothing was abandoned\" " +
			"(§9.92 invariant 2)")
	}
	for _, face := range []string{"public.session_turns_hot", "public.session_turns"} {
		if !strings.Contains(code, face) {
			t.Fatalf("abandoned_turn.go (comments stripped) never names %s — the landing "+
				"pad updates only one face of session_turns (§9.92 invariant 2)", face)
		}
	}
	// ⚠ The per-face Contains checks above are **satisfied by a substring**:
	// "public.session_turns" is a prefix of "public.session_turns_hot", so a
	// file that writes only hot still passes them. That is why the earlier
	// draft of this gate passed a one-face mutation. The check that actually
	// has teeth is the face-list shape below, which requires both names to
	// appear as separate quoted literals in the same []string literal.
	//
	// Ordered both ways is accepted; requiring one fixed order would make this
	// gate fire on a harmless reordering.
	twoFaces := strings.Contains(code, `[]string{"public.session_turns_hot", "public.session_turns"}`) ||
		strings.Contains(code, `[]string{"public.session_turns", "public.session_turns_hot"}`)
	if !twoFaces {
		t.Fatalf("the iterated face list does not contain BOTH session_turns faces as "+
			"separate literals.\n"+
			"Writing only one face silently updates 0 rows on the other, and 0 rows is "+
			"indistinguishable from \"nothing was abandoned\" — that is the half-fixed bug "+
			"this project has hit repeatedly (§9.92 invariant 2).\n"+
			"got: %s", faceListOf(code))
	}
}

// faceListOf extracts the []string{…} literal used for the face iteration, for
// error messages. Returns a placeholder when there is none.
func faceListOf(code string) string {
	i := strings.Index(code, "for _, tbl := range ")
	if i < 0 {
		return "<no range over tbl>"
	}
	rest := code[i:]
	if j := strings.Index(rest, "\n"); j > 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// TestAbandonedTurnNeverInsertsAPlaceholderRow pins invariant 3.
//
// The failure this prevents is quiet: a t0 placeholder would insert with
// success=false, and the later terminal write would be swallowed by
// ON CONFLICT DO NOTHING — leaving a permanently wrong row that still counts
// as a real turn in every aggregate.
func TestAbandonedTurnNeverInsertsAPlaceholderRow(t *testing.T) {
	src := readRepoFile(t, "domains/hooks/observability/telemetry/abandoned_turn.go")

	// ⚠ The INSERT can only appear as a **concatenated** literal here
	// (`+tbl+`), because the table name is looped over. A check for the
	// literal "INSERT INTO public.session_turns" therefore passes on a file
	// that inserts a placeholder row — which is precisely the mutation this
	// assertion exists to catch, and it is why the first draft of this gate
	// let that mutation through.
	//
	// So match on the part that cannot vary: the statement keyword + INTO,
	// with no regard to which face follows. The loop over tables is asserted
	// separately by TestAbandonedTurnMarkTouchesBothTurnsFaces.
	codeOnly := stripGoComments(src)
	if regexpInsertIntoTurns.MatchString(codeOnly) {
		t.Fatalf("abandoned_turn.go contains an INSERT into session_turns.\n" +
			"session_turns is idempotent on (tenant_id, request_id, partition_date) " +
			"with ON CONFLICT DO NOTHING, so a t0 placeholder row would be pinned at " +
			"success=false forever and swallow the terminal enrichment " +
			"(§9.92 invariant 3). The landing pad must be a flag on the terminal row, " +
			"not a row of its own.")
	}

	// The UPDATE must be a real UPDATE (so it is idempotent and cannot create
	// rows), and must not assert success/status_code — the terminal row already
	// carries those and overwriting them would be the same corruption by another
	// route.
	if !strings.Contains(src, "SET is_abandoned = TRUE") {
		t.Fatalf("abandoned_turn.go does not set is_abandoned = TRUE — " +
			"the landing pad's only write should be that one assignment")
	}
	for _, forbidden := range []string{"success =", "status_code =", "error_kind ="} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("abandoned_turn.go writes %q — the landing pad must not touch the "+
				"terminal row's outcome columns; it only marks the row (§9.92 invariant 3)", forbidden)
		}
	}
}

// TestMigration820ExistsOnBothFacesAndIsRegistered guards the five-point sync.
//
// §9.64.10 recorded the shape this prevents: the migration file sits in the
// canonical tree, every package test is green, and no machine ever runs it —
// because a running gateway does not apply startup migrations; the installer
// is the only executor.
func TestMigration820ExistsOnBothFacesAndIsRegistered(t *testing.T) {
	const rel = "sql/migrations/startup/820_session_turns_abandoned_marker.sql"
	mig := readRepoFile(t, rel)

	// Both faces must be altered in the migration itself, not just mentioned.
	for _, face := range []string{"public.session_turns\n", "public.session_turns_hot\n"} {
		if !strings.Contains(mig, "ALTER TABLE "+face) &&
			!strings.Contains(mig, "ALTER TABLE "+strings.TrimSpace(face)) {
			t.Fatalf("migration 820 does not ALTER %s — one face would be left without "+
				"the column and every UPDATE against it would fail 42703", strings.TrimSpace(face))
		}
	}

	// The five-point sync, checked against the real files rather than trusted.
	embedRel := filepath.Join("installer/cmd/llm-gw-installer/embeddata/startup",
		"820_session_turns_abandoned_marker.sql")
	a := readRepoFile(t, rel)
	b := readRepoFile(t, embedRel)
	if string(a) != string(b) {
		t.Fatalf("embeddata copy differs from canonical source — the installer would " +
			"apply a different migration than the one under review (five-point sync point 1)")
	}

	mainGo := readRepoFile(t, "installer/cmd/llm-gw-installer/main.go")
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/820_session_turns_abandoned_marker.sql") {
		t.Fatalf("main.go has no //go:embed for migration 820 (five-point sync point 2)")
	}
	if !strings.Contains(mainGo, `"startup/820_session_turns_abandoned_marker.sql"`) {
		t.Fatalf("main.go has no embeddedSQLFiles entry for migration 820 (point 3)")
	}

	runner := readRepoFile(t, "installer/internal/dbinit/runner.go")
	if !strings.Contains(runner, `"820_session_turns_abandoned_marker.sql"`) {
		t.Fatalf("runner.go StartupFiles has no entry for migration 820 (point 4) — " +
			"the installer would never apply it, and a running gateway does not " +
			"apply startup migrations either (§9.64.10)")
	}

	tsv := readRepoFile(t, "sql/schema/installed_startup_migrations.tsv")
	if !strings.Contains(tsv, "820_session_turns_abandoned_marker.sql") {
		t.Fatalf("installed_startup_migrations.tsv has no entry for migration 820 (point 5)")
	}
}

// braceDepth returns the net nesting depth of `{` minus `}` in src.
//
// Counts are computed on a comment- and string-stripped view: a brace inside a
// comment or a string literal must not move the depth, or the measurement
// answers a question about the prose rather than about the code. This is the
// same "use the parser's view, not a raw grep" discipline the Go gates in this
// repo use via stripGoCommentsKeepLines.
func braceDepth(src string) int {
	depth := 0
	inLineComment, inBlockComment, inStr, inRune := false, false, false, false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		n := byte(0)
		if i+1 < len(src) {
			n = src[i+1]
		}
		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
			}
		case inBlockComment:
			if c == '*' && n == '/' {
				inBlockComment = false
				i++
			}
		case inStr:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
		case inRune:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '\'' {
				inRune = false
			}
		default:
			switch {
			case c == '/' && n == '/':
				inLineComment = true
				i++
			case c == '/' && n == '*':
				inBlockComment = true
				i++
			case c == '"':
				inStr = true
			case c == '\'':
				inRune = true
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
		}
	}
	return depth
}

// readRepoFileBytes is readRepoFile without the string round-trip, so callers
// that want byte-exact comparison can have it.
func readRepoFileBytes(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

// stripGoComments removes // and /* */ comments while preserving every other
// byte, so that indexes and string literals still line up with the original.
//
// Kept deliberately naive: this is a *gate input*, not a parser. It exists so
// that "the identifier is mentioned in prose" cannot be mistaken for "the
// identifier is used in code", which is the failure this gate was rewritten to
// stop (the first draft passed a one-face mutation for exactly that reason).
func stripGoComments(src string) string {
	var out []byte
	inStr, inRune, esc := false, false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if inRune {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '\'' {
				inRune = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '\'' {
			inRune = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// regexpInsertIntoTurns matches an INSERT aimed at a session_turns face, in
// either the literal or the concatenated form. Written to survive the
// concatenation because the table name is looped over:
//
//	INSERT INTO public.session_turns_hot
//	INSERT INTO ` + tbl + `
var regexpInsertIntoTurns = regexp.MustCompile(
	"INSERT\\s+INTO\\s+" + "`?\\s*(\\+tbl\\+|public\\.session_turns)")
