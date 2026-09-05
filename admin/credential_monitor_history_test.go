package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func modelHistoryRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"ts", "source", "triggered_by", "event", "probe_status", "http_status",
		"error_code", "error_message", "actor", "reason",
	}).AddRow(
		time.Date(2026, 8, 20, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)),
		"auto", "scheduled", "broke", "http_5xx", 503, "upstream_error", "gateway timeout", nil, nil,
	).AddRow(
		time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC),
		"manual", nil, "offline", nil, nil, nil, nil, "operator", "",
	)
}

func TestRunModelHistory_TenantIsolationAndMapping(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`WITH auto_events[\s\S]*mpr\.tenant_id = \$4[\s\S]*al\.tenant_id = \$4[\s\S]*`).
		WithArgs(17, "gpt-4o", 20, "tenant-a").
		WillReturnRows(modelHistoryRows())

	events, err := runModelHistory(context.Background(), mock, 17, "gpt-4o", 20, "tenant-a")
	if err != nil {
		t.Fatalf("runModelHistory: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].TS != "2026-08-20T04:00:00Z" || events[0].Source != "auto" || events[0].HTTPStatus == nil || *events[0].HTTPStatus != 503 {
		t.Fatalf("auto event mapping drifted: %+v", events[0])
	}
	if events[1].Reason != nil || events[1].Actor == nil || *events[1].Actor != "operator" {
		t.Fatalf("manual event normalization drifted: %+v", events[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}

func TestRunModelHistory_QueryAndIterationErrorsPropagate(t *testing.T) {
	t.Run("query error", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()

		mock.ExpectQuery(`WITH auto_events[\s\S]*`).
			WithArgs(17, "gpt-4o", 20, "tenant-a").
			WillReturnError(errors.New("query unavailable"))
		if _, err := runModelHistory(context.Background(), mock, 17, "gpt-4o", 20, "tenant-a"); err == nil || !strings.Contains(err.Error(), "query failed") {
			t.Fatalf("expected wrapped query error, got %v", err)
		}
	})

	t.Run("iteration error", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()

		rows := modelHistoryRows().CloseError(errors.New("stream interrupted"))
		mock.ExpectQuery(`WITH auto_events[\s\S]*`).
			WithArgs(17, "gpt-4o", 20, "tenant-a").
			WillReturnRows(rows)
		events, err := runModelHistory(context.Background(), mock, 17, "gpt-4o", 20, "tenant-a")
		if err == nil || !strings.Contains(err.Error(), "rows iteration failed") {
			t.Fatalf("expected wrapped iteration error, got %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected rows before iteration error, got %d", len(events))
		}
	})
}
