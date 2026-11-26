package metricsstore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// ★ 2026-11-26 12h 审计轮（P2-2）：就绪判定原先只看 backfill_complete && updated_at
// ⇒ 投影器停摆 / pending 无界积压时长期 200 假绿。本组用例钉住新口径：
// 水位 60 分钟不动 / 积压头超 60 分钟未消化 ⇒ 503 + stale 标志。

func TestProjectionReadinessStaleWatermark(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	stale := now.Add(-2 * time.Hour)

	rows := func(backfill bool, updatedAt, pendingSince time.Time) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"backfill_complete", "checked_at", "updated_at", "pending_since"}).
			AddRow(backfill, now, updatedAt, pendingSince)
	}

	serve := func() (*httptest.ResponseRecorder, projectionReadiness) {
		recorder := httptest.NewRecorder()
		ProjectionReadinessHandler(db).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz/request-metrics", nil))
		var state projectionReadiness
		if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
			t.Fatalf("decode readiness body: %v", err)
		}
		return recorder, state
	}

	// ① 健康态：200 且无 stale。
	mock.ExpectQuery("SELECT backfill_complete").WillReturnRows(rows(true, fresh, fresh.Add(-time.Minute)))
	rec, state := serve()
	if rec.Code != http.StatusOK || state.Stale || state.Reason != "" {
		t.Fatalf("healthy watermark judged stale: HTTP %d state=%+v", rec.Code, state)
	}

	// ② 水位停摆：updated_at 2 小时前 ⇒ 503 + stale 原因。
	mock.ExpectQuery("SELECT backfill_complete").WillReturnRows(rows(true, stale, fresh))
	rec, state = serve()
	if rec.Code != http.StatusServiceUnavailable || !state.Stale || state.Reason == "" {
		t.Fatalf("stalled watermark not downgraded: HTTP %d state=%+v", rec.Code, state)
	}

	// ③ 积压无界：pending_since 2 小时前 ⇒ 503 + stale（即便水位在动）。
	mock.ExpectQuery("SELECT backfill_complete").WillReturnRows(rows(true, fresh, stale))
	rec, state = serve()
	if rec.Code != http.StatusServiceUnavailable || !state.Stale {
		t.Fatalf("unconsumed backlog not downgraded: HTTP %d state=%+v", rec.Code, state)
	}

	// ④ 变异自证（反向）：阈值判断被去掉时 ②③ 会变 200 假绿 ⇒ 本组用例红。
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestProjectionIsStaleUnit(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ok := true
	if stale, _ := projectionIsStale(now, ptrTime(now.Add(-time.Minute)), ptrTime(now.Add(-time.Minute))); stale {
		ok = false
	}
	if stale, _ := projectionIsStale(now, ptrTime(now.Add(-projectionStaleThreshold-time.Second)), nil); !stale {
		ok = false
	}
	if stale, _ := projectionIsStale(now, nil, ptrTime(now.Add(-projectionStaleThreshold-time.Second))); !stale {
		ok = false
	}
	if stale, _ := projectionIsStale(now, nil, nil); stale {
		ok = false
	}
	if !ok {
		t.Fatal("projectionIsStale thresholds wrong")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
