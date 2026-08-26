package telemetry

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestRequestLogMainTableExcludesBodyColumns pins the migration-573 contract:
// the main request_logs_hot table must never reference the three body columns
// (request_body / response_body / outbound_body) in its INSERT or UPDATE
// statements. Bodies live exclusively in the request_logs_bodies_hot side
// table via upsertRequestLogBodies.
//
// 2026-08-26 incident (SQLSTATE 42703, 154/245): a client.go build lagged
// behind migration 573 (which dropped the three body columns) and every
// telemetry write died with `column "request_body" of relation
// "request_logs_hot" does not exist` — request_logs_hot went effectively
// empty while the fallback path silently absorbed the data. This offline
// guard fails the moment anyone reintroduces those columns into the main-table
// SQL, whether by code regression or by a migration that re-adds the columns
// and tempts a future "quick fix".
//
// It reads client.go source only — no network, no database.
func TestRequestLogMainTableExcludesBodyColumns(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	srcStr := string(src)
	forbidden := []string{"request_body", "response_body", "outbound_body"}

	stmts := map[string]struct {
		signature string
		sqlAnchor string
	}{
		"insertRequestLog": {"func (c *Client) insertRequestLog", "INSERT INTO request_logs_hot ("},
		"updateRequestLog": {"func (c *Client) updateRequestLog", "UPDATE request_logs_hot"},
	}

	for name, st := range stmts {
		fn := extractFunc(srcStr, st.signature)
		if fn == "" {
			t.Fatalf("%s not found in client.go", st.signature)
		}
		sqlStart := strings.Index(fn, st.sqlAnchor)
		if sqlStart < 0 {
			t.Fatalf("%s: SQL anchor %q not found", name, st.sqlAnchor)
		}
		sqlEnd := strings.Index(fn[sqlStart:], "`") + sqlStart
		if sqlEnd < sqlStart {
			t.Fatalf("%s: unterminated SQL literal", name)
		}
		// Strip SQL comments so mentions inside -- comments don't mask a real
		// regression (and comments documenting the side-table move don't trip it).
		sql := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(fn[sqlStart:sqlEnd], "")

		for _, col := range forbidden {
			if regexp.MustCompile(`\b` + col + `\b`).MatchString(sql) {
				t.Errorf("%s: main-table SQL references forbidden body column %q — bodies belong in request_logs_bodies_hot (migration 573 / 42703 regression)", name, col)
			}
		}

		// For the INSERT, also assert the explicit column list so the failure
		// message names the offending position, not just the identifier.
		if name == "insertRequestLog" {
			cols := parseIdentList(sql, sqlIndex(sql, "(", 0)+1, sqlIndex(sql, ") VALUES", 0))
			if len(cols) == 0 {
				t.Fatalf("insertRequestLog: column list parse failed")
			}
			for i, col := range cols {
				for _, bad := range forbidden {
					if col == bad {
						t.Errorf("insertRequestLog: column #%d is %q (migration 573 dropped it from request_logs_hot)", i+1, bad)
					}
				}
			}
		}
	}
}
