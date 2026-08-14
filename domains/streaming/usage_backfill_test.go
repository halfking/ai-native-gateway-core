package streaming

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// fakeUsageCorrector records the correction call for the backfill tests.
type fakeUsageCorrector struct {
	rows   int64
	err    error
	called bool
	entry  *telemetry.RequestLogEntry
}

func (f *fakeUsageCorrector) CorrectEstimatedUsage(ctx context.Context, entry *telemetry.RequestLogEntry) (int64, error) {
	f.called = true
	f.entry = entry
	return f.rows, f.err
}

func backfillEntry(prompt, completion int) *telemetry.RequestLogEntry {
	return &telemetry.RequestLogEntry{
		RequestID:        "req-backfill",
		PromptTokens:     &prompt,
		CompletionTokens: &completion,
	}
}

// estimated → 真实回填：request_logs 行被修正（rows=1）后，format_anomalies
// 行补写真实 completion tokens 与 usage_source='corrected'。
func TestRunUsageBackfill_CorrectedRowBackfillsAnomalies(t *testing.T) {
	corrector := &fakeUsageCorrector{rows: 1}
	db := &mockDB{}
	recorder := NewFormatAnomalyRecorder(db)

	runUsageBackfill(context.Background(), corrector, recorder, backfillEntry(120, 340))

	if !corrector.called {
		t.Fatal("expected CorrectEstimatedUsage to be called")
	}
	if corrector.entry.RequestID != "req-backfill" {
		t.Errorf("correction request_id = %q", corrector.entry.RequestID)
	}
	if !db.execCalled {
		t.Fatal("expected anomaly backfill exec after corrected row")
	}
	if db.execArgs[0] != "req-backfill" || db.execArgs[1] != 340 {
		t.Errorf("anomaly backfill args = %v, want [req-backfill 340]", db.execArgs)
	}
}

// 无 estimated 行（rows=0，即行本就是 llm/corrected 或不存在）：不动 format_anomalies。
func TestRunUsageBackfill_NoEstimatedRowLeavesAnomaliesAlone(t *testing.T) {
	corrector := &fakeUsageCorrector{rows: 0}
	db := &mockDB{}
	recorder := NewFormatAnomalyRecorder(db)

	runUsageBackfill(context.Background(), corrector, recorder, backfillEntry(10, 20))

	if !corrector.called {
		t.Fatal("expected CorrectEstimatedUsage to be called")
	}
	if db.execCalled {
		t.Error("anomaly backfill must not run when no row was corrected")
	}
}

// 回填失败只告警不传播：corrector 报错时安静返回。
func TestRunUsageBackfill_CorrectorErrorIsBestEffort(t *testing.T) {
	corrector := &fakeUsageCorrector{err: errors.New("db down")}
	db := &mockDB{}
	recorder := NewFormatAnomalyRecorder(db)

	runUsageBackfill(context.Background(), corrector, recorder, backfillEntry(10, 20))

	if db.execCalled {
		t.Error("anomaly backfill must not run after correction error")
	}
}

// 无真实 usage（token 全 nil）或缺 request_id：完全不发起回填。
func TestRunUsageBackfill_SkipsWithoutRealUsage(t *testing.T) {
	corrector := &fakeUsageCorrector{rows: 1}
	recorder := NewFormatAnomalyRecorder(&mockDB{})
	prompt := 1

	runUsageBackfill(context.Background(), corrector, recorder, &telemetry.RequestLogEntry{RequestID: "req-x"})
	runUsageBackfill(context.Background(), corrector, recorder, &telemetry.RequestLogEntry{RequestID: "", PromptTokens: &prompt})

	if corrector.called {
		t.Error("correction must be skipped without real usage / request_id")
	}
}

// syncExecDB is a mutex-guarded exec fake for tests that observe async writes.
type syncExecDB struct {
	mu      sync.Mutex
	called  bool
	query   string
	args    []any
	execErr error
}

func (m *syncExecDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
	m.query = query
	m.args = args
	return pgconn.CommandTag{}, m.execErr
}

func (m *syncExecDB) snapshot() (bool, string, []any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.called, m.query, m.args
}

// 异步 wrapper：不阻塞调用方，最终完成回填。
func TestBackfillEstimatedUsage_AsyncCompletes(t *testing.T) {
	corrector := &fakeUsageCorrector{rows: 1}
	db := &syncExecDB{}
	recorder := NewFormatAnomalyRecorder(db)

	backfillEstimatedUsage(corrector, recorder, backfillEntry(50, 60))

	deadline := time.Now().Add(2 * time.Second)
	for {
		if called, _, args := db.snapshot(); called {
			if args[0] != "req-backfill" || args[1] != 60 {
				t.Errorf("anomaly backfill args = %v, want [req-backfill 60]", args)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("async backfill did not complete in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// nil 依赖下不 panic（生产路径上 corrector/anomalies 可为 nil）。
func TestBackfillEstimatedUsage_NilSafe(t *testing.T) {
	backfillEstimatedUsage(nil, nil, backfillEntry(1, 2))
	backfillEstimatedUsage(&fakeUsageCorrector{rows: 1}, nil, backfillEntry(1, 2))
	backfillEstimatedUsage(&fakeUsageCorrector{rows: 1}, NewFormatAnomalyRecorder(&mockDB{}), nil)
}
