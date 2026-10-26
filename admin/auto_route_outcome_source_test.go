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
		// ★ 2026-10-20（迁移 890，v1 族真删）：settle worker 恒读会话族
		// ⇒ 探针（outcome-freshness）也恒读会话族，与写门无关。
		// 原判据是「写门开 ⇒ 读 v1 热表」——那个默认值在 890 之后指向一张
		// **已删除的表** ⇒ 42P01 ⇒ 探针恒报「读不到」。
		{"dual write", true, "SELECT MAX(ts) FROM session_turns_hot"},
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
