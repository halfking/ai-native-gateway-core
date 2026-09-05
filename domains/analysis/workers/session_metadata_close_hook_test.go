package workers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/domains/sessionsummary"
)

type stubMessageLoader struct {
	msgs []sessionsummary.SessionMessage
	err  error
}

func (s stubMessageLoader) GetSessionMessages(context.Context, string, string) ([]sessionsummary.SessionMessage, error) {
	return s.msgs, s.err
}

func TestSessionMetadataCloseHook_UpsertsFinal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	store := sessionmeta.NewMetadataStore(mock)
	result := sessionmeta.Extract(sessionmeta.Input{
		Messages: []sessionmeta.Message{{Role: "user", Content: "final hook"}},
	})

	mock.ExpectQuery(`SELECT input_hash, updated_at`).
		WithArgs("default", "gw_hook_1", sessionmeta.StatusProvisional).
		WillReturnError(pgx.ErrNoRows)

	mock.ExpectExec(`INSERT INTO public\.session_analysis_metadata`).
		WithArgs("default", "gw_hook_1", sessionmeta.SchemaVersion, sessionmeta.StatusFinal, result.InputHash, pgxmock.AnyArg(), "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	hook := NewSessionMetadataCloseHook(store, stubMessageLoader{
		msgs: []sessionsummary.SessionMessage{{Role: "user", Content: "final hook"}},
	}, nil)
	if err := hook.OnSessionClosed(context.Background(), "default", "gw_hook_1"); err != nil {
		t.Fatalf("OnSessionClosed: %v", err)
	}
	if hook.Stats()["written"] != 1 {
		t.Fatalf("written = %d, want 1", hook.Stats()["written"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSessionMetadataCloseHook_LoadErrorIsBestEffort(t *testing.T) {
	hook := NewSessionMetadataCloseHook(sessionmeta.NewMetadataStore(nil), stubMessageLoader{err: context.Canceled}, nil)
	if err := hook.OnSessionClosed(context.Background(), "default", "gw_hook_1"); err != nil {
		t.Fatalf("OnSessionClosed: %v", err)
	}
	if hook.Stats()["failed"] != 1 {
		t.Fatalf("failed = %d, want 1", hook.Stats()["failed"])
	}
}

func TestSessionMetadataCloseHook_NilDepsNoOp(t *testing.T) {
	var hook *SessionMetadataCloseHook
	if err := hook.OnSessionClosed(context.Background(), "default", "gw_hook_1"); err != nil {
		t.Fatalf("nil hook: %v", err)
	}
}
