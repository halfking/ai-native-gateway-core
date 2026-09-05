package titlestore

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func stateRow(title string, source string, priority int, deleted bool, token int64) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"title", "deleted", "deleted_at", "fencing_token",
		"lease_owner", "lease_expires_at", "source", "source_priority", "source_task_id",
	}).AddRow(title, deleted, nil, token, "", nil, source, priority, "")
}

func TestSource_ProvisionalTitleBelowAuto(t *testing.T) {
	info := Source(SourceProvisionalTitle)
	if info.priority != SourcePriorityProvisional || info.priority >= SourcePriorityAuto {
		t.Fatalf("provisional priority = %d, want %d < %d", info.priority, SourcePriorityProvisional, SourcePriorityAuto)
	}
}

// OnlyIfEmpty must reject inside the locked transaction when a title already
// exists — this closes the Get→Begin TOCTOU race for arrival-time writes.
func TestBeginMutation_OnlyIfEmpty_RejectsExistingTitle(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO public\.session_title_states`).
		WithArgs("t1", "s1", SourceProvisionalTitle, SourcePriorityProvisional, "task-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery(`FOR UPDATE`).
		WithArgs("t1", "s1").
		WillReturnRows(stateRow("已有标题", SourceManual, SourcePriorityManual, false, 3))

	store := New(mock)
	_, err = store.BeginMutation(context.Background(), Claim{
		TenantID: "t1", SessionID: "s1", Owner: "w", TTL: 5,
		Source: SourceProvisionalTitle, SourcePriority: SourcePriorityProvisional,
		OnlyIfEmpty: true, TaskID: "task-1",
	})
	if !errors.Is(err, ErrPriority) {
		t.Fatalf("err = %v, want ErrPriority", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}

// The refined auto-title (priority 10) must be allowed to replace an active
// provisional title (priority 5) — the arrival title never blocks refinement.
func TestBeginMutation_AutoTitleOverridesProvisional(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO public\.session_title_states`).
		WithArgs("t1", "s2", SourceAutoTitle, SourcePriorityAuto, "task-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	// Existing row written by the provisional source with priority 5.
	mock.ExpectQuery(`FOR UPDATE`).
		WithArgs("t1", "s2").
		WillReturnRows(stateRow("临时标题", SourceProvisionalTitle, SourcePriorityProvisional, false, 4))
	mock.ExpectExec(`UPDATE public\.session_title_states`).
		WithArgs("t1", "s2", int64(5), pgxmock.AnyArg(), pgxmock.AnyArg(), SourceAutoTitle, SourcePriorityAuto, "task-1", false).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	store := New(mock)
	claim, err := store.BeginMutation(context.Background(), Claim{
		TenantID: "t1", SessionID: "s2", Owner: "refine", TTL: 30,
		Source: SourceAutoTitle, SourcePriority: SourcePriorityAuto, TaskID: "task-1",
	})
	if err != nil {
		t.Fatalf("auto-title over provisional: %v", err)
	}
	if claim.Token != 5 {
		t.Fatalf("token = %d, want 5", claim.Token)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}

// A late provisional arrival (priority 5) must NOT overwrite the refined
// auto-title (priority 10) even without OnlyIfEmpty — priority guard alone
// already protects the refined writer.
func TestBeginMutation_ProvisionalCannotOverwriteAutoTitle(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO public\.session_title_states`).
		WithArgs("t1", "s3", SourceProvisionalTitle, SourcePriorityProvisional, "task-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery(`FOR UPDATE`).
		WithArgs("t1", "s3").
		WillReturnRows(stateRow("LLM 精炼标题", SourceAutoTitle, SourcePriorityAuto, false, 7))

	store := New(mock)
	_, err = store.BeginMutation(context.Background(), Claim{
		TenantID: "t1", SessionID: "s3", Owner: "late-arrival", TTL: 5,
		Source: SourceProvisionalTitle, SourcePriority: SourcePriorityProvisional,
		OnlyIfEmpty: true, TaskID: "task-1",
	})
	if !errors.Is(err, ErrPriority) {
		t.Fatalf("err = %v, want ErrPriority", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}
