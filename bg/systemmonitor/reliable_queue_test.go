// Package bg/systemmonitor — reliable_queue_test.go
//
// Reliability tests for the round-3 Redis queue hardening:
//   - atomic submit (no orphan hash / queue on success)
//   - claim → crash → reclaim restores the task (the core bug: LPOP used to lose it)
//   - complete fencing rejects a stale worker after reclaim
//   - double complete is safe
//
// All tests run against miniredis so the Lua scripts execute for real.
package systemmonitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestQueue(t *testing.T) (*Queue, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	scripts, err := LoadScripts(context.Background(), rdb)
	if err != nil {
		t.Fatalf("LoadScripts: %v", err)
	}
	return NewQueue(rdb, scripts), mr, rdb
}

func processingLen(t *testing.T, rdb *redis.Client) int64 {
	t.Helper()
	n, err := rdb.LLen(context.Background(), RedisKeyProcessing).Result()
	if err != nil {
		t.Fatalf("LLen processing: %v", err)
	}
	return n
}

func sampleTask() *Task {
	return &Task{
		TaskType:     TaskTypeHTTPPing,
		Automaticity: AutomaticityMandatory,
		Source:       SourceButtonAll,
		CredentialID: 42,
		ProviderID:   7,
		RawModel:     "gpt-test",
		MaxAttempts:  3,
	}
}

// TestAtomicSubmitNoOrphan: after Submit the task hash AND the queue entry
// exist together; there is no half-committed state.
func TestAtomicSubmitNoOrphan(t *testing.T) {
	q, mr, _ := newTestQueue(t)
	ctx := context.Background()

	task := sampleTask()
	if err := q.Submit(ctx, task); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if task.ID == 0 {
		t.Fatal("task.ID not assigned")
	}
	// Hash present with the assigned id.
	if !mr.Exists(task.HashKey()) {
		t.Fatalf("task hash missing for id %d (orphan queue)", task.ID)
	}
	// Queue non-empty.
	if n, _ := q.QueueSize(ctx); n != 1 {
		t.Fatalf("expected queue size 1, got %d", n)
	}
}

// TestClaimThenCrashReclaimRestores: the headline reliability fix. A task is
// claimed (removed from the ready queue, placed in processing). Simulating a
// worker crash (no Complete), advancing the lease clock, a Reclaim sweep must
// put the task back into the ready queue so it is not lost.
func TestClaimThenCrashReclaimRestores(t *testing.T) {
	q, mr, rdb := newTestQueue(t)
	ctx := context.Background()
	ctx = contextWithWorkerID(ctx, "worker-A")

	if err := q.Submit(ctx, sampleTask()); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	claimed, err := q.Claim(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("Claim: task=%v err=%v", claimed, err)
	}
	// Task left the ready queue, now lives in processing.
	if n, _ := q.QueueSize(ctx); n != 0 {
		t.Fatalf("ready queue should be empty after claim, got %d", n)
	}
	if n := processingLen(t, rdb); n != 1 {
		t.Fatalf("processing lane should hold 1 task, got %d", n)
	}

	// No reclaim yet (lease still valid).
	if n, err := q.Reclaim(ctx); err != nil || n != 0 {
		t.Fatalf("premature reclaim: n=%d err=%v", n, err)
	}

	// Simulate the worker crash: fast-forward past the lease TTL.
	mr.FastForward(RedisLeaseTTL + time.Second)

	n, err := q.Reclaim(ctx)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reclaimed task, got %d", n)
	}
	// Task is back in the ready queue, gone (by id) from processing.
	if size, _ := q.QueueSize(ctx); size != 1 {
		t.Fatalf("ready queue should hold the restored task, got %d", size)
	}
}

// TestCompleteFencedRejectsStaleOwner: after reclaim re-dispatches a task to
// worker-B, worker-B completes successfully. The original worker-A's late
// Complete must be rejected (ErrLeaseLost) and must NOT overwrite worker-B's
// terminal status.
func TestCompleteFencedRejectsStaleOwner(t *testing.T) {
	q, mr, _ := newTestQueue(t)
	ctx := context.Background()

	ctxA := contextWithWorkerID(ctx, "worker-A")
	ctxB := contextWithWorkerID(ctx, "worker-B")

	if err := q.Submit(ctx, sampleTask()); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	taskA, err := q.Claim(ctxA)
	if err != nil || taskA == nil {
		t.Fatalf("Claim A: %v / %v", taskA, err)
	}
	staleToken := taskA.leaseToken
	if staleToken == "" {
		t.Fatal("claim did not capture lease_token")
	}

	// Worker A stalls; lease expires; reclaim restores the task.
	mr.FastForward(RedisLeaseTTL + time.Second)
	if n, _ := q.Reclaim(ctx); n != 1 {
		t.Fatalf("expected 1 reclaimed, got %d", n)
	}

	// Worker B claims and completes the restored task.
	taskB, err := q.Claim(ctxB)
	if err != nil || taskB == nil {
		t.Fatalf("Claim B: %v / %v", taskB, err)
	}
	if err := q.Complete(ctxB, taskB, TaskStatusSuccess, nil); err != nil {
		t.Fatalf("Complete B: %v", err)
	}

	// Worker A's late complete must be fenced off.
	taskA.leaseToken = staleToken
	if err := q.Complete(ctxA, taskA, TaskStatusFailed, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale owner complete should return ErrLeaseLost, got %v", err)
	}

	// The authoritative status stays worker-B's success.
	status := mr.HGet(taskB.HashKey(), "status")
	if status != string(TaskStatusSuccess) {
		t.Fatalf("status overwritten by stale owner: %q", status)
	}
}

// TestCompleteRemovesFromProcessing: a normal complete clears the recovery
// lane so reclaim never restores an already-finished task.
func TestCompleteRemovesFromProcessing(t *testing.T) {
	q, mr, rdb := newTestQueue(t)
	ctx := context.Background()
	ctx = contextWithWorkerID(ctx, "worker-X")

	if err := q.Submit(ctx, sampleTask()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	task, err := q.Claim(ctx)
	if err != nil || task == nil {
		t.Fatalf("Claim: %v / %v", task, err)
	}
	if err := q.Complete(ctx, task, TaskStatusSuccess, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if n := processingLen(t, rdb); n != 0 {
		t.Fatalf("processing lane should be empty after complete, got %d", n)
	}
	if n, _ := q.QueueSize(ctx); n != 0 {
		t.Fatalf("ready queue should be empty after complete, got %d", n)
	}
	// Reclaim after the fact finds nothing.
	mr.FastForward(RedisLeaseTTL + time.Second)
	if n, _ := q.Reclaim(ctx); n != 0 {
		t.Fatalf("reclaim should find nothing after complete, got %d", n)
	}
}

// TestRequeueClearsLeaseState: Requeue re-enqueues a failed task for retry and
// must not leave a stale processing copy that reclaim would later double-restore.
func TestRequeueClearsLeaseState(t *testing.T) {
	q, _, rdb := newTestQueue(t)
	ctx := context.Background()
	ctx = contextWithWorkerID(ctx, "worker-Y")

	if err := q.Submit(ctx, sampleTask()); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	task, err := q.Claim(ctx)
	if err != nil || task == nil {
		t.Fatalf("Claim: %v / %v", task, err)
	}
	// Requeue without completing (the monitor does this on retryable failure).
	next := time.Now().Add(time.Second)
	if err := q.Requeue(ctx, task, next); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	// No lingering processing copy.
	if n := processingLen(t, rdb); n != 0 {
		t.Fatalf("processing lane should be cleared on requeue, got %d", n)
	}
	// Task is back in the ready queue (waiting for scheduled_at).
	if n, _ := q.QueueSize(ctx); n != 1 {
		t.Fatalf("ready queue should hold requeued task, got %d", n)
	}
}
