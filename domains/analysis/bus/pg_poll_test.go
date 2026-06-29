package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
)

func TestNewPGPollFunc_NilDBReturnsEmpty(t *testing.T) {
	poll := NewPGPollFunc(nil, []analysis.EventType{analysis.EventRequestCompleted}, 10)
	events, err := poll(context.Background(), 1)
	if err != nil || len(events) != 0 {
		t.Fatalf("want empty/nil, got %d/%v", len(events), err)
	}
}

func TestNewPGMarkFunc_NilDBNoop(t *testing.T) {
	mark := NewPGMarkFunc(nil, nil)
	if err := mark(context.Background(), "evt-1", "intent_worker", nil); err != nil {
		t.Fatalf("nil db mark err = %v", err)
	}
}

func TestNewPGPollFunc_LoadsEvents(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rows := pgxmock.NewRows([]string{"event_id", "type", "tenant_id", "session_id", "request_id", "payload", "occurred_at"}).
		AddRow("ev-1", string(analysis.EventRequestCompleted), "t1", "s1", "r1", []byte(`{"user_content":"hi"}`), time.Now())
	mock.ExpectQuery("SELECT event_id, type, tenant_id").
		WithArgs([]string{string(analysis.EventRequestCompleted)}, 5).
		WillReturnRows(rows)

	poll := NewPGPollFunc(mock, []analysis.EventType{analysis.EventRequestCompleted}, 10)
	events, err := poll(context.Background(), 5)
	if err != nil || len(events) != 1 {
		t.Fatalf("got %d events / err=%v", len(events), err)
	}
	if events[0].EventID != "ev-1" || events[0].SessionID != "s1" || events[0].RequestID != "r1" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNewPGMarkFunc_FailurePath(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("UPDATE analysis_events").
		WithArgs("ev-1", "boom", "intent_worker").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mark := NewPGMarkFunc(mock, nil)
	if err := mark(context.Background(), "ev-1", "intent_worker", errors.New("boom")); err != nil {
		t.Fatalf("mark err = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
