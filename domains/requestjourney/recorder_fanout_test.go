package requestjourney

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRecorderSlowPostgresDoesNotStarveRedis guards the fan-out contract: each
// store has its own worker, so a blocked PostgreSQL write must not delay the
// Redis hot projection.
func TestRecorderSlowPostgresDoesNotStarveRedis(t *testing.T) {
	pgStore := &fakeJourneyWriter{started: make(chan JourneyEvent, 1), release: make(chan struct{})}
	redisStore := &fakeJourneyWriter{started: make(chan JourneyEvent, 1)}
	recorder := newRecorder(NewProjection(DefaultConfig()), redisStore, pgStore, recorderOptions{
		queueCapacity: 4,
		writeTimeout:  time.Second,
	})
	defer func() {
		close(pgStore.release)
		closeRecorder(t, recorder)
	}()

	if err := recorder.Apply(context.Background(), testJourneyEvent("tenant-a", "request-1", 1)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-redisStore.started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("blocked PostgreSQL write starved Redis projection")
	}
}

// TestRecorderQueueFullOnlyDropsAffectedStore guards fan-out isolation at the
// enqueue boundary: a saturated Redis queue must not prevent PostgreSQL from
// receiving the same events.
func TestRecorderQueueFullOnlyDropsAffectedStore(t *testing.T) {
	redisStore := &fakeJourneyWriter{started: make(chan JourneyEvent, 1), release: make(chan struct{})}
	pgStore := &fakeJourneyWriter{}
	recorder := newRecorder(NewProjection(DefaultConfig()), redisStore, pgStore, recorderOptions{
		queueCapacity: 1,
		writeTimeout:  time.Second,
	})
	defer func() {
		close(redisStore.release)
		closeRecorder(t, recorder)
	}()

	for seq := int64(1); seq <= 3; seq++ {
		event := testJourneyEvent("tenant-a", "request-1", seq)
		event.OccurredAt = event.OccurredAt.Add(time.Duration(seq) * time.Second)
		if err := recorder.Apply(context.Background(), event); err != nil {
			t.Fatalf("Apply(seq=%d) error = %v", seq, err)
		}
		// Keep the unblocked PostgreSQL queue drained between applies so only
		// the saturated Redis side can drop.
		if err := waitUntil(time.Second, func() bool {
			return int64(len(pgStore.snapshot())) == seq
		}); err != nil {
			t.Fatalf("postgres writes after seq=%d = %d", seq, len(pgStore.snapshot()))
		}
	}

	// The first event occupies the blocked Redis worker; the second fills the
	// one-slot queue; the third must be dropped by Redis only.
	if _, err := waitForStoreStats(recorder, "redis", func(s StoreStats) bool {
		return s.EnqueueDrops == 1
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := waitForStoreStats(recorder, "postgres", func(s StoreStats) bool {
		return s.EnqueueDrops == 0 && s.WriteDrops == 0
	}); err != nil {
		t.Fatalf("postgres stats: %v", err)
	}
}

func waitForStoreStats(recorder *Recorder, store string, match func(StoreStats) bool) (StoreStats, error) {
	var matched StoreStats
	err := waitUntil(2*time.Second, func() bool {
		for _, stats := range recorder.StoreStats() {
			if stats.Store == store && match(stats) {
				matched = stats
				return true
			}
		}
		return false
	})
	if err != nil {
		return StoreStats{}, err
	}
	return matched, nil
}

func waitUntil(timeout time.Duration, condition func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	if condition() {
		return nil
	}
	return errTimeoutWaiting
}

var errTimeoutWaiting = fmtTimeoutError()

func fmtTimeoutError() error {
	return errors.New("condition not met before timeout")
}

// flakyJourneyWriter fails the first failLimit calls, then succeeds.
type flakyJourneyWriter struct {
	mu        sync.Mutex
	failLimit int
	calls     int
	events    []JourneyEvent
}

func (f *flakyJourneyWriter) Apply(_ context.Context, event JourneyEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failLimit {
		return errors.New("transient store failure")
	}
	f.events = append(f.events, event)
	return nil
}

// TestRecorderReplaysTransientWriteFailures guards the outbox contract: a
// store that fails transiently must eventually receive the event through
// replay, without dropping it or degrading the observation.
func TestRecorderReplaysTransientWriteFailures(t *testing.T) {
	store := &flakyJourneyWriter{failLimit: 2}
	recorder := newRecorder(NewProjection(DefaultConfig()), store, nil, recorderOptions{
		queueCapacity: 4, writeTimeout: time.Second,
		maxAttempts: 3, initialBackoff: time.Millisecond,
	})
	var reported atomic.Int64
	recorder.SetErrorHandler(func(error) { reported.Add(1) })

	if err := recorder.Apply(context.Background(), testJourneyEvent("tenant-a", "request-1", 1)); err != nil {
		t.Fatal(err)
	}

	stats, err := waitForStoreStats(recorder, "redis", func(s StoreStats) bool {
		return s.Replayed == 1 && s.WriteDrops == 0
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 0 || len(store.events) != 1 {
		t.Fatalf("first-attempt writes/store events = %d/%d", stats.Written, len(store.events))
	}
	if reported.Load() != 0 {
		t.Fatalf("replayed write reported %d errors", reported.Load())
	}
	closeRecorder(t, recorder)
	journey, err := recorder.memory.Detail("tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if journey.ObservationStatus != ObservationComplete {
		t.Fatalf("replayed journey degraded: %q", journey.ObservationStatus)
	}
}

// TestRecorderDropsAfterRetryExhaustion guards the bounded-retry contract:
// after the final attempt the write is dropped once, reported once, and the
// in-memory observation is marked degraded instead of pretending completeness.
func TestRecorderDropsAfterRetryExhaustion(t *testing.T) {
	store := &alwaysFailingWriter{}
	memory := NewProjection(DefaultConfig())
	recorder := newRecorder(memory, store, nil, recorderOptions{
		queueCapacity: 4, writeTimeout: 50 * time.Millisecond,
		maxAttempts: 2, initialBackoff: time.Millisecond,
	})
	var reported atomic.Int64
	recorder.SetErrorHandler(func(error) { reported.Add(1) })

	if err := recorder.Apply(context.Background(), testJourneyEvent("tenant-a", "request-1", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := waitForStoreStats(recorder, "redis", func(s StoreStats) bool {
		return s.WriteDrops == 1 && s.Pending == 0
	}); err != nil {
		t.Fatal(err)
	}
	if reported.Load() != 1 {
		t.Fatalf("drop reports = %d, want 1", reported.Load())
	}
	closeRecorder(t, recorder)
	journey, err := memory.Detail("tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if journey.ObservationStatus != ObservationDegraded {
		t.Fatalf("exhausted journey status = %q", journey.ObservationStatus)
	}
}

type alwaysFailingWriter struct{}

func (*alwaysFailingWriter) Apply(context.Context, JourneyEvent) error {
	return errors.New("store down")
}

// TestRecorderCountsSequenceGapsPerStore guards gap observability: when a
// store never receives seq 2 (dropped upstream), writing seq 3 must surface
// the hole instead of silently presenting a contiguous-looking history.
func TestRecorderCountsSequenceGapsPerStore(t *testing.T) {
	store := &fakeJourneyWriter{}
	recorder := newRecorder(NewProjection(DefaultConfig()), store, nil, recorderOptions{
		queueCapacity: 4, writeTimeout: time.Second,
	})
	defer closeRecorder(t, recorder)

	first := testJourneyEvent("tenant-a", "request-1", 1)
	if err := recorder.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	stats, err := waitForStoreStats(recorder, "redis", func(s StoreStats) bool { return s.Written == 1 })
	if err != nil {
		t.Fatal(err)
	}
	if stats.SequenceGaps != 0 {
		t.Fatalf("initial gaps = %d", stats.SequenceGaps)
	}

	skipped := testJourneyEvent("tenant-a", "request-1", 3)
	skipped.Stage = StageUpstream
	if err := recorder.Apply(context.Background(), skipped); err != nil {
		t.Fatal(err)
	}
	if stats, err = waitForStoreStats(recorder, "redis", func(s StoreStats) bool {
		return s.Written == 2 && s.SequenceGaps == 1
	}); err != nil {
		t.Fatalf("gap after skipped seq: %v", err)
	}
}
