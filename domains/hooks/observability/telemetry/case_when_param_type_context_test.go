package telemetry

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestCaseWhenPlaceholderHasTypeContext guards one specific, silent shape:
//
//	a bare $N sitting in a CASE **WHEN** condition has no type context.
//
// PostgreSQL determines a parameter's type while parsing. In
// `col = CASE WHEN $N ... THEN col ELSE ... END`, the WHEN condition is
// evaluated *before* the CASE's result type is known, and the WHEN condition
// is not part of the set that gets unified against `col`. So $N stays
// `unknown` and the whole statement is rejected at parse time:
//
//	ERROR: could not determine data type of parameter $98  (SQLSTATE 42P08)
//
// Measured 2026-10-02 against request_logs_hot, one PREPARE per shape, with
// **no** declared parameter types — which is exactly what pgx sends:
//
//	CASE WHEN $1     IS NULL THEN request_class ELSE $1      → ERROR
//	CASE WHEN $1::text IS NULL THEN request_class ELSE $1      → OK
//	CASE WHEN $1     IS NULL THEN request_class ELSE $1::text → ERROR
//	COALESCE($1, request_class)                               → OK
//
// Two facts follow, and both are load-bearing:
//   - the cast belongs on the occurrence **in the WHEN**; a cast on the ELSE
//     branch does not rescue it (line 3 above);
//   - a parameter that is a direct argument of COALESCE/GREATEST/LEAST/NULLIF
//     is fine, because those resolve all arguments against one common type.
//
// WHY A STATIC GATE AND NOT THE REAL-DB TEST: TestRequestClassPGRoundTrip
// found this, but it skips without TEST_PG_DSN — and the surrounding file
// already records that a gate which is structurally asleep still reports "ok".
// A production write path failing this way is swallowed as a WARN by the
// persist side: no crash, no alert, scheduled requests simply never persist
// request_class/due_at. So the shape needs a check that runs offline.
//
// NOT TESTED HERE: whether the resulting statement is semantically correct —
// only that it is *parseable*. A cast can always be the wrong type; that is a
// different question, and the real-DB test is the one that answers it.
func TestCaseWhenPlaceholderHasTypeContext(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}

	// Strip line comments first: a commented-out fragment can mention $N and
	// must not be judged, exactly like a real parser would not see it.
	cleaned := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(string(src), "")

	caseWhen := regexp.MustCompile(`(?is)CASE\s+WHEN\b(.*?)\bTHEN\b`)
	typedBy := regexp.MustCompile(`(?is)\b(COALESCE|GREATEST|LEAST|NULLIF)\s*\([^()]*$`)
	param := regexp.MustCompile(`\$\d+`)

	// No operator ambiguity: this counts occurrences, not matched strings.
	offending := map[int]int{}

	for _, m := range caseWhen.FindAllStringSubmatchIndex(cleaned, -1) {
		cond := cleaned[m[2]:m[3]]
		for _, pm := range param.FindAllStringIndex(cond, -1) {
			if strings.HasPrefix(cond[pm[1]:], "::") {
				continue // explicit cast: type context supplied
			}
			// Inside COALESCE(...$N / GREATEST(...$N etc. the function supplies it.
			if typedBy.MatchString(cond[:pm[0]]) {
				continue
			}
			abs := m[2] + pm[0]
			offending[abs]++
		}
	}

	if len(offending) == 0 {
		return
	}

	// Report each distinct line once, with the offending tokens, so the reader
	// can act without re-deriving which occurrence is at fault.
	seen := map[int]bool{}
	for at := range offending {
		line := 1 + strings.Count(cleaned[:at], "\n")
		if seen[line] {
			continue
		}
		seen[line] = true
		lineText := ""
		if i := strings.IndexByte(cleaned[line:], '\n'); i >= 0 {
			lineText = strings.TrimSpace(cleaned[line : line+i])
		}
		t.Errorf("client.go:%d: CASE 的 WHEN 判据里有裸占位符，没有类型上下文："+
			"\n    %s\n"+
			"    PostgreSQL 会在这里报 `could not determine data type of parameter $N`"+
			"（SQLSTATE 42P08）并拒绝整条语句。\n"+
			"    修法：给 WHEN 判据里那个 $N 加显式 cast（$N::text）。"+
			"只给 THEN/ELSE 分支加 cast **不**管用——实测证过。", line, lineText)
	}
}
