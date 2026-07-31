package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// TestDBWriterStopIdempotent pins the contract that DBWriter.Stop is safe to
// call more than once. The main gateway shutdown path intentionally calls
// Stop twice: once via the explicit shutdown goroutine after
// telemetryClient.Stop (request-flow Step 3, spec §6.3) and again via the
// deferred sessionState.Shutdown() at the end of main(). The first close of
// stopCh must close the flush loop; subsequent calls must be no-ops so
// we don't panic on close-of-closed-channel.
//
// In production Start() is always called before Stop() (the gateway calls
// sessionDBWriter.Start(ctx) at session_state_init.go:66 and only then runs
// the gateway). Calling Stop without Start is not a supported path because
// doneCh would never be closed; we don't exercise it here.
func TestDBWriterStopIdempotent(t *testing.T) {
	// Case A: sequential double-stop after Start.
	wA := NewDBWriter(nil, 1, time.Hour)
	require.NotNil(t, wA)
	wA.Start(nil)

	wA.Stop() // first stop — closes stopCh, joins flush loop
	wA.Stop() // second stop — sync.Once short-circuits, must not panic
	wA.Stop() // third stop — also safe

	// Case B: many concurrent Stops on the same writer must serialise
	// through sync.Once and never double-close stopCh.
	wB := NewDBWriter(nil, 1, time.Hour)
	require.NotNil(t, wB)
	wB.Start(nil)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wB.Stop()
		}()
	}
	wg.Wait()
}

// TestDBWriterCtxCancelDrainsPending (Round 3, 2026-07-28) pins the
// contract that when the runFlushLoop's context is cancelled, the
// in-flight pending batch is drained via FlushAll before the goroutine
// returns. Previously the ctx.Done branch returned without flushing —
// if the gateway cancelled ctx (e.g. telemetryClient.Stop) while
// entries were queued, those session_credential_rotations rows would
// be lost.
//
// We use pgxmock so flushSession's Begin/QueryRow/Exec/Commit sequence
// hits a deterministic mock. The post-condition is that the pending
// map is empty after the loop exits (doneCh closed).
func TestDBWriterCtxCancelDrainsPending(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { mock.Close() })

	// Each enqueued session is drained by flushSession: Begin → QueryRow
	// (SELECT COALESCE(MAX(seq), 0) FROM session_credential_rotations WHERE
	// session_id = $1) → Exec (INSERT) → Commit. We register enough
	// sequences for both enqueued sessions plus a safety margin.
	for i := 0; i < 4; i++ {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT COALESCE").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"max_seq"}).AddRow(0))
		mock.ExpectExec("INSERT INTO session_credential_rotations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectCommit()
	}

	w := newDBWriterWithDB(mock, 10, time.Hour)
	require.NotNil(t, w)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	w.Enqueue("sess-drain-1", CredRotationEntry{
		CredentialID: 1,
		Model:        "gpt-4",
		Provider:     "openai",
		StartedAt:    time.Now(),
		Turns:        1,
		PromptTokens: 10,
	})
	w.Enqueue("sess-drain-2", CredRotationEntry{
		CredentialID: 2,
		Model:        "gpt-4",
		Provider:     "openai",
		StartedAt:    time.Now(),
		Turns:        2,
		PromptTokens: 20,
	})

	// Sanity: pending has both sessions queued.
	w.mu.Lock()
	require.Len(t, w.pending, 2, "expected two pending sessions before cancel")
	w.mu.Unlock()

	// Cancel the context — the flush loop should drain and exit.
	cancel()

	// Wait for the loop to exit (doneCh closed). The defer FlushAll in
	// runFlushLoop must complete before doneCh is closed, so a closed
	// doneCh guarantees the drain ran.
	select {
	case <-w.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runFlushLoop did not exit within 2s after ctx cancel")
	}

	// Post-condition: pending map is empty (drained by FlushAll).
	w.mu.Lock()
	pendingLen := len(w.pending)
	w.mu.Unlock()
	require.Equal(t, 0, pendingLen, "pending map must be empty after ctx-cancel drain")
}

// TestDBWriterStopChDrainsPending pins the same drain contract for the
// stopCh path (separate from ctx-cancel so we can be sure both branches
// hit the same defer).
func TestDBWriterStopChDrainsPending(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { mock.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"max_seq"}).AddRow(0))
	mock.ExpectExec("INSERT INTO session_credential_rotations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	w := newDBWriterWithDB(mock, 10, time.Hour)
	require.NotNil(t, w)

	w.Start(context.Background())

	w.Enqueue("sess-stopch", CredRotationEntry{
		CredentialID: 99,
		Model:        "gpt-4",
		Provider:     "openai",
		StartedAt:    time.Now(),
		Turns:        1,
	})

	w.mu.Lock()
	require.Len(t, w.pending, 1)
	w.mu.Unlock()

	w.Stop()

	w.mu.Lock()
	pendingLen := len(w.pending)
	w.mu.Unlock()
	require.Equal(t, 0, pendingLen, "pending map must be empty after stopCh drain")
}

// TestDBWriterStopAndCtxCancelInterleave pins the contract that the
// drain defer works even when stopCh fires while ctx is being cancelled
// concurrently. The gateway shutdown path is exactly this: telemetry
// cancels its own ctx and main() closes the writer's stopCh. The drain
// must happen on whichever signal wins, but the post-condition (empty
// pending) is the same.
func TestDBWriterStopAndCtxCancelInterleave(t *testing.T) {
	// Each iteration uses a fresh writer + fresh mock. We just need the
	// mock to provide enough Begin/Query/Exec/Commit pairs that the
	// drain's flushSession can run end-to-end. One entry → one Begin +
	// SELECT MAX(seq) + INSERT + Commit is enough.
	const iterations = 16
	var drainsObserved int64
	for i := 0; i < iterations; i++ {
		mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
		require.NoError(t, err)

		mock.ExpectBegin()
		mock.ExpectQuery("SELECT COALESCE").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"max_seq"}).AddRow(0))
		mock.ExpectExec("INSERT INTO session_credential_rotations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectCommit()

		w := newDBWriterWithDB(mock, 10, time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		w.Start(ctx)

		w.Enqueue("sess-race", CredRotationEntry{
			CredentialID: 42,
			Model:        "gpt-4",
			Provider:     "openai",
			StartedAt:    time.Now(),
			Turns:        1,
		})
		// Half the time, fire cancel first; the rest, fire stopCh first.
		if i%2 == 0 {
			cancel()
			w.Stop()
		} else {
			w.Stop()
			cancel()
		}
		// After both signals, drain must have run.
		w.mu.Lock()
		pendingLen := len(w.pending)
		w.mu.Unlock()
		if pendingLen == 0 {
			atomic.AddInt64(&drainsObserved, 1)
		}
		mock.Close()
	}
	require.Greater(t, atomic.LoadInt64(&drainsObserved), int64(iterations-1),
		"drain must run on every iteration, observed %d/%d", drainsObserved, iterations)
}
