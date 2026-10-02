package sessionv2mirror

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestBackfillProjectionKeysMatchEntryTags pins the GAP-2 backfill
// contract: every jsonb_build_object key in scripts/audit/
// mirror_outbox_backfill.sql must be a real RequestLogEntry json tag.
//
// Why: the reaper unmarshals outbox payloads straight into the struct, so
// a key that drifts from a tag (column renamed, tag renamed) is silently
// ignored and that field is lost from every backfilled row — exactly the
// silent-drift class the storage gate exists to catch.
//
// Known-good exceptions: the backfill projects request_logs.ts as
// 'event_at' (the entry has no ts tag). 'success' is a real tag and is
// projected from the v1 success column — 2026-09-30 修订，此前它取自
// is_final_success（语义是「本请求是否抢到本会话的最终成功标记」，不是
// 「本请求是否成功」）。那次改口径只在 is_final_success IS TRUE 的范围内
// 等价，但一放宽选取口径就会把成功请求重放成失败，故在此钉死来源。
func TestBackfillProjectionKeysMatchEntryTags(t *testing.T) {
	const sqlPath = "../../scripts/audit/mirror_outbox_backfill.sql"
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatalf("read backfill SQL: %v", err)
	}

	allowed := map[string]bool{
		"request_id": true, // projected verbatim from the column of the same name
		"event_at":   true, // ts → event_at rename (the entry has no ts tag)
	}

	tags := map[string]bool{}
	typ := reflect.TypeOf(telemetry.RequestLogEntry{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			tags[name] = true
		}
	}

	keyRe := regexp.MustCompile(`'([a-z0-9_]+)',\s*$`)
	var bad []string
	for _, line := range strings.Split(string(raw), "\n") {
		m := keyRe.FindStringSubmatch(strings.TrimSpace(strings.TrimRight(line, ",")))
		if m == nil {
			continue
		}
		key := m[1]
		if allowed[key] || tags[key] {
			continue
		}
		bad = append(bad, key)
	}
	if len(bad) > 0 {
		t.Fatalf("backfill payload keys that are not RequestLogEntry json tags: %v", bad)
	}
}

// TestBackfillScopeReachesTerminalNonFinalSuccessRows pins the 2026-09-30
// selection-scope correction.
//
// The backfill used to select `is_final_success IS TRUE`, on the assumption
// that final-success rows are the ones worth replaying. Measured on the real
// DB that assumption is wrong twice over:
//
//   - 35-day window, success=true terminal rows: 320 with
//     is_final_success=true, **329 with is_final_success=false** — the latter
//     are real business turns the hook did mirror, silently out of scope.
//   - the 1,459 pre-712 genuine_loss rows have **zero** is_final_success=true
//     members (a failure turn can never claim final success), so the old
//     scope hit them 0 times while looking like it was "cleaning up".
//
// Scope must therefore key on terminal-ness + having a session header, with
// the anti-join against session_turns doing the actual selection.
func TestBackfillScopeReachesTerminalNonFinalSuccessRows(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/audit/mirror_outbox_backfill.sql")
	if err != nil {
		t.Fatalf("read backfill SQL: %v", err)
	}
	body := string(raw)

	if strings.Contains(body, "WHERE is_final_success IS TRUE") {
		t.Fatal("backfill must not select on is_final_success IS TRUE — that scope reaches 0 of the pre-712 loss rows and 329 successful turns it should have replayed")
	}
	// Both v1 legs (hot + parent) must carry the terminal + session-header
	// filter, or one leg silently contributes nothing.
	for _, leg := range []string{"FROM public.request_logs_hot", "FROM public.request_logs\n"} {
		if !strings.Contains(body, leg) {
			t.Fatalf("expected both v1 legs to survive the scope change, missing %q", leg)
		}
	}
	if n := strings.Count(body, "gw_session_id <> ''"); n < 2 {
		t.Fatalf("both v1 legs must filter on a non-empty gw_session_id, found %d", n)
	}
	if n := strings.Count(body, "<> 'in_progress'"); n < 2 {
		t.Fatalf("both v1 legs must apply the terminal test (mirroring isTerminalFailure), found %d", n)
	}
	// success must come from the v1 success column, never from
	// is_final_success. True on the old scope, actively wrong on the new one.
	if !strings.Contains(body, "'success',            m.v1json -> 'success'") {
		t.Fatal("payload success must be projected from the v1 success column, not is_final_success")
	}
	if strings.Contains(body, "'success',            m.v1json -> 'is_final_success'") {
		t.Fatal("payload success must not be projected from is_final_success — it marks the session's final-success turn, not whether this request succeeded")
	}
	// is_final_success stays out of the payload: the reaper unmarshals into
	// RequestLogEntry, which has no such field.
	if !strings.Contains(body, "- 'ts' - 'is_final_success' - 'id'") {
		t.Fatal("is_final_success must remain stripped from the payload")
	}
}

// TestBackfillProjectionCastsNonScalarColumns is the type half of the
// payload contract; TestBackfillProjectionKeysMatchEntryTags is the name half.
//
// Name-correct but type-wrong is the worse failure of the two: the reaper
// does json.Unmarshal straight into RequestLogEntry, so a jsonb OBJECT
// landing on a *string field aborts the whole decode. The row then
// dead-letters and — before 2026-09-30 — could never be recovered, because
// the backfill's ON CONFLICT DO NOTHING meant a re-run after the fix was a
// no-op.
//
// Derived by cross-checking every projected column against the live schema on
// 2026-09-30 (request_logs / request_logs_hot × information_schema.data_type
// against the Go field type). auto_decision was the single mismatch:
// jsonb in the database, *string in the struct, non-null on ~2.15M rows —
// it would have dead-lettered essentially every backfilled row on a real
// dataset, and did dead-letter 12 here.
//
// A jsonb → *string column must be projected with an explicit ::text.
// json.RawMessage and []string targets are fine as-is.
func TestBackfillProjectionCastsNonScalarColumns(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/audit/mirror_outbox_backfill.sql")
	if err != nil {
		t.Fatalf("read backfill SQL: %v", err)
	}
	body := string(raw)

	// Columns whose DB type is non-scalar but whose Go field is a plain
	// string — each needs an explicit cast in BOTH v1 legs.
	mustCast := []string{"auto_decision"}
	for _, col := range mustCast {
		casted := regexp.MustCompile(regexp.QuoteMeta(col) + `::text AS ` + regexp.QuoteMeta(col))
		if n := len(casted.FindAllString(body, -1)); n != 2 {
			t.Errorf("column %q needs `::text AS %s` in both v1 legs (jsonb → *string aborts the reaper's decode); found %d", col, col, n)
		}
	}

	// A bare projection of such a column reintroduces the bug. Strip the
	// properly-cast form and the comments first, then assert the bare name is
	// gone — matching on "the word must not appear" would false-positive on
	// the cast's own `AS auto_decision` and on the prose.
	stripped := regexp.MustCompile(`(?m)--[^\n]*`).ReplaceAllString(body, "")
	stripped = regexp.MustCompile(regexp.QuoteMeta("auto_decision::text AS auto_decision")).ReplaceAllString(stripped, "")
	if regexp.MustCompile(`\bauto_decision\b`).MatchString(stripped) {
		t.Error("auto_decision is projected without the ::text cast; the reaper will fail to decode it and dead-letter the row")
	}
}
