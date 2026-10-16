package admin

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type recordingOutcomeDB struct {
	sql string
	row fakeOutcomeRow
}

func (d *recordingOutcomeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	d.sql = sql
	return &d.row
}

func TestOutcomeFreshnessFollowsActiveSettleSource(t *testing.T) {
	for _, tc := range []struct {
		name         string
		writeEnabled bool
		wantSQL      string
	}{
		{"dual write", true, "SELECT MAX(ts) FROM _request_logs_hot"},
		{"session only", false, "SELECT MAX(ts) FROM session_turns_hot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withRequestLogsWriteSetting(t, tc.writeEnabled)
			now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
			db := &recordingOutcomeDB{row: fakeOutcomeRow{val: &now}}
			got := queryOutcomeFreshness(context.Background(), db, now)
			if db.sql != tc.wantSQL {
				t.Fatalf("query = %q, want %q", db.sql, tc.wantSQL)
			}
			if got.Stale || got.Reason != outcomeReasonLive || !got.Available {
				t.Fatalf("fresh active source reported incorrectly: %+v", got)
			}
		})
	}
}
