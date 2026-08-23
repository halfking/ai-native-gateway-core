package admin

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/internal/titlestore"
)

func TestMaybeGenerateProvisionalMetadata_RespectsEnabledGate(t *testing.T) {
	gen := &AutoTitleGenerator{
		enabled: false,
		handler: &Handler{
			titleStore:            titlestore.New(nil),
			analysisMetadataStore: sessionmeta.NewMetadataStore(nil),
		},
	}
	body := []byte(`{"messages":[{"role":"user","content":"修复登录失败"}]}`)
	gen.MaybeGenerateProvisionalMetadata("tenant-a", "gw_disabled_gate", "task-1", sessionmeta.Input{RequestBody: body})

	gen.enabled = true
	gen.handler = nil
	gen.MaybeGenerateProvisionalMetadata("tenant-a", "gw_disabled_gate", "task-1", sessionmeta.Input{RequestBody: body})
}

func TestCommitProvisionalArrival_SkipsTitleWhenAlreadyExists(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	result := sessionmeta.Extract(sessionmeta.Input{
		Messages: []sessionmeta.Message{{Role: "user", Content: "修复登录"}},
	})

	mock.ExpectExec(`INSERT INTO public\.session_analysis_metadata`).
		WithArgs("tenant-a", "gw_existing_title", sessionmeta.SchemaVersion, sessionmeta.StatusProvisional, result.InputHash, pgxmock.AnyArg(), "task-new").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	// commitProvisionalTitle: Begin → INSERT state → SELECT FOR UPDATE finds
	// an existing manual title; OnlyIfEmpty rejects inside the transaction.
	rows := pgxmock.NewRows([]string{
		"title", "deleted", "deleted_at", "fencing_token", "lease_owner", "lease_expires_at", "source", "source_priority", "source_task_id",
	}).AddRow("已有标题", false, nil, int64(1), "", nil, "manual", 40, "task-old")
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO public\.session_title_states`).
		WithArgs("tenant-a", "gw_existing_title", titlestore.SourceProvisionalTitle, titlestore.SourcePriorityProvisional, "task-new").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs("tenant-a", "gw_existing_title").WillReturnRows(rows)

	gen := &AutoTitleGenerator{
		enabled: true,
		handler: &Handler{
			titleStore:            titlestore.New(mock),
			analysisMetadataStore: sessionmeta.NewMetadataStore(mock),
		},
	}
	gen.commitProvisionalArrival("tenant-a", "gw_existing_title", "task-new", result)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}

func TestCommitProvisionalArrival_WritesMetadataWithoutTitle(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	result := sessionmeta.Extract(sessionmeta.Input{
		Messages: []sessionmeta.Message{{Role: "system", Content: "You are helpful"}},
	})
	if result.Title != "" {
		t.Fatalf("expected empty title for system-only input, got %q", result.Title)
	}

	mock.ExpectExec(`INSERT INTO public\.session_analysis_metadata`).
		WithArgs("tenant-a", "gw_no_user", sessionmeta.SchemaVersion, sessionmeta.StatusProvisional, result.InputHash, pgxmock.AnyArg(), "task-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	gen := &AutoTitleGenerator{
		enabled: true,
		handler: &Handler{
			analysisMetadataStore: sessionmeta.NewMetadataStore(mock),
		},
	}
	gen.commitProvisionalArrival("tenant-a", "gw_no_user", "task-1", result)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}

// TestCheckSessionHasTitle_IgnoresProvisionalSource pins the lifecycle rule:
// a provisional arrival title must not short-circuit the refined LLM title.
func TestCheckSessionHasTitle_IgnoresProvisionalSource(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	provisionalRow := pgxmock.NewRows([]string{
		"tenant_id", "scoped_session_id", "title", "deleted", "deleted_at",
		"fencing_token", "lease_owner", "lease_expires_at", "source", "source_priority", "source_task_id",
	}).AddRow("tenant-a", "gw_p", "临时标题", false, nil, int64(3), "", nil, titlestore.SourceProvisionalTitle, titlestore.SourcePriorityProvisional, "task-1")
	mock.ExpectQuery(`FROM public\.session_title_states`).
		WithArgs("tenant-a", "gw_p").
		WillReturnRows(provisionalRow)

	gen := &AutoTitleGenerator{enabled: true, handler: &Handler{titleStore: titlestore.New(mock)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	has, err := gen.checkSessionHasTitle(ctx, "tenant-a", "task-1", "gw_p")
	if err != nil {
		t.Fatalf("checkSessionHasTitle: %v", err)
	}
	if has {
		t.Fatal("provisional title must not count as an existing title")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB calls: %v", err)
	}
}
