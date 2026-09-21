package executors

import (
	"testing"
	"time"
)

// Audit-2026-08-29 (§5 hardening): the fp release worker is a sync.Once
// global goroutine started in init(). A panic in Manager.Release would
// kill the worker permanently, causing fpReleaseQueue to fill to 1024 and
// all subsequent releaseFpLease calls to fall back to the synchronous 1s
// timeout path — adding hot-path latency and leaking fp slots. This test
// exercises that the worker survives a panic and continues processing
// subsequent jobs.

// We can't mock Manager directly because it's a concrete type and Release
// is called on job.m, so we test by observing that the queue continues to
// drain after we inject a panicking job. We directly push to fpReleaseQueue
// (same package) and use a sentinel Manager that we monitor.

func TestFpReleaseWorkerSurvivesPanic(t *testing.T) {
	// We'll push jobs directly to fpReleaseQueue and observe that they drain.
	// To simulate a panic, we push a job whose manager's Release will panic.
	// Since Manager is a concrete type, we can't mock it in-place. Instead,
	// we rely on the fact that the worker loop now has defer recover(), and
	// we verify the queue keeps draining after the panic by checking queue length.

	// First, confirm the worker is running by sending a normal job (nil manager
	// is safe — the worker calls m.Release which will be a nil pointer deref,
	// but the recover() will catch it).
	initialQueueLen := len(fpReleaseQueue)

	// Push a job that will panic (nil manager -> nil pointer deref in Release)
	fpReleaseQueue <- fpReleaseJob{m: nil, lease: nil}

	// Wait for the panic job to be processed. The worker should log the panic
	// and continue. We can't observe the log directly, but we can confirm the
	// queue drained.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fpReleaseQueue) == initialQueueLen {
			// Queue drained back to initial length.
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(fpReleaseQueue) != initialQueueLen {
		t.Fatalf("fp release worker likely died after panic: queue length = %d, want %d", len(fpReleaseQueue), initialQueueLen)
	}

	// Now push a second job to confirm the worker is still alive.
	fpReleaseQueue <- fpReleaseJob{m: nil, lease: nil}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fpReleaseQueue) == initialQueueLen {
			// Second job also drained — worker survived the first panic.
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("fp release worker did not process second job: queue length = %d, want %d", len(fpReleaseQueue), initialQueueLen)
}
