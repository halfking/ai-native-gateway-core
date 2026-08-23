package admin

import (
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/internal/titlestore"
)

func TestProvisionalTitleCommitBlocked(t *testing.T) {
	tests := []struct {
		name string
		st   titlestore.State
		err  error
		want bool
	}{
		{name: "existing title blocks commit", st: titlestore.State{Title: "已有标题"}, err: nil, want: true},
		{name: "deleted tombstone allows commit", st: titlestore.State{Title: "old", Deleted: true}, err: nil, want: false},
		{name: "empty title allows commit", st: titlestore.State{Title: "  "}, err: nil, want: false},
		{name: "get error allows commit attempt", st: titlestore.State{}, err: titlestore.ErrNotConfigured, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := provisionalTitleCommitBlocked(tc.st, tc.err); got != tc.want {
				t.Fatalf("provisionalTitleCommitBlocked() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMaybeGenerateProvisionalMetadata_RespectsEnabledGate(t *testing.T) {
	gen := &AutoTitleGenerator{
		enabled: false,
		handler: &Handler{titleStore: titlestore.New(nil)},
	}
	body := []byte(`{"messages":[{"role":"user","content":"修复登录失败"}]}`)
	gen.MaybeGenerateProvisionalMetadata("tenant-a", "gw_disabled_gate", "task-1", sessionmeta.Input{RequestBody: body})

	gen.enabled = true
	gen.handler = nil
	gen.MaybeGenerateProvisionalMetadata("tenant-a", "gw_disabled_gate", "task-1", sessionmeta.Input{RequestBody: body})
}

func TestCommitProvisionalTitle_SkipsWhenTitleAlreadyExists(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows([]string{
		"tenant_id", "scoped_session_id", "title", "deleted", "deleted_at",
		"fencing_token", "lease_owner", "lease_expires_at", "source", "source_priority", "source_task_id",
	}).AddRow("tenant-a", "gw_existing_title", "已有标题", false, nil, int64(1), "", nil, "manual", 40, "task-old")
	mock.ExpectQuery(`FROM public\.session_title_states`).
		WithArgs("tenant-a", "gw_existing_title").
		WillReturnRows(rows)

	gen := &AutoTitleGenerator{
		enabled: true,
		handler: &Handler{titleStore: titlestore.New(mock)},
	}
	gen.commitProvisionalTitle("tenant-a", "gw_existing_title", "task-new", "新标题不应写入")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls after existing title: %v", err)
	}
}

func TestMaybeGenerateProvisionalMetadata_SkipsEmptyExtractedTitle(t *testing.T) {
	gen := &AutoTitleGenerator{
		enabled: true,
		handler: &Handler{titleStore: titlestore.New(nil)},
	}
	gen.MaybeGenerateProvisionalMetadata("tenant-a", "gw_no_user", "task-1", sessionmeta.Input{
		RequestBody: []byte(`{"messages":[{"role":"system","content":"You are helpful"}]}`),
	})
	time.Sleep(50 * time.Millisecond)
}
