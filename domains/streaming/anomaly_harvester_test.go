package streaming

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeBackfillTx captures the sweep SQL so the harvester's batch backfill
// contract can be pinned without a live PG.
type fakeBackfillTx struct {
	pgx.Tx
	execSQL []string
}

func (f *fakeBackfillTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execSQL = append(f.execSQL, sql)
	return pgconn.NewCommandTag("UPDATE 0"), nil
}

func (f *fakeBackfillTx) Commit(ctx context.Context) error { return nil }

// TestBackfillActualTokensSweepCoversCorrectedRows pins the CO-2 audit fix
// (doc 20 C-P2-2): the batch sweep must pick up request_logs rows in both
// terminal real-usage states. Matching only 'llm' left a permanent gap for
// rows the online path corrected to 'corrected' whose anomaly backfill
// failed (single warn-and-drop in usage_backfill.go).
func TestBackfillActualTokensSweepCoversCorrectedRows(t *testing.T) {
	tx := &fakeBackfillTx{}
	if err := backfillActualTokensSweep(context.Background(), tx); err != nil {
		t.Fatalf("backfillActualTokensSweep() error = %v", err)
	}
	var sweep string
	for _, s := range tx.execSQL {
		if len(s) > 40 {
			sweep = s
		}
	}
	if sweep == "" {
		t.Fatal("sweep UPDATE was not issued")
	}
	if !strings.Contains(sweep, "'llm'") || !strings.Contains(sweep, "'corrected'") {
		t.Errorf("sweep SQL must match request_logs usage_source IN ('llm','corrected'), got:\n%s", sweep)
	}
	if !strings.Contains(sweep, "= 'estimated'") {
		t.Errorf("sweep must only touch estimated anomaly rows, got:\n%s", sweep)
	}
}
