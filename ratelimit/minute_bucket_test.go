package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMinuteBucketAdmissionCapsCurrentAndWaitingCapacity(t *testing.T) {
	a := NewMinuteBucketAdmission()
	a.window = time.Second

	for i := 0; i < 2; i++ {
		result, err := a.Admit(context.Background(), 7, 2)
		if err != nil || !result.Admitted {
			t.Fatalf("request %d: result=%+v err=%v", i, result, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan error, 1)
	go func() {
		_, err := a.Admit(ctx, 7, 2)
		waiting <- err
	}()
	time.Sleep(time.Millisecond)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	waiting2 := make(chan error, 1)
	go func() {
		_, err := a.Admit(ctx2, 7, 2)
		waiting2 <- err
	}()
	time.Sleep(time.Millisecond)

	if _, err := a.Admit(context.Background(), 7, 2); !errors.Is(err, ErrMinuteBucketFull) {
		t.Fatalf("request beyond 2L capacity: got %v", err)
	}
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: got %v", err)
	}
	cancel2()
	if err := <-waiting2; !errors.Is(err, context.Canceled) {
		t.Fatalf("second cancelled waiter: got %v", err)
	}
}

func TestMinuteBucketAdmissionReleasesFIFOAtNextBucket(t *testing.T) {
	a := NewMinuteBucketAdmission()
	clock := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	a.clock = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	a.window = 10 * time.Millisecond

	_, _ = a.Admit(context.Background(), 7, 1)
	firstDone := make(chan error, 1)
	firstQueued := make(chan struct{}, 1)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	go func() {
		_, err := a.admit(firstCtx, 7, 1, func(AdmissionResult) { firstQueued <- struct{}{} })
		firstDone <- err
	}()
	<-firstQueued
	clockMu.Lock()
	clock = clock.Add(a.window)
	clockMu.Unlock()
	time.Sleep(2 * a.window)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first waiter was not released")
	}
}

func TestMinuteBucketAdmissionNotifiesWhenRequestStartsWaiting(t *testing.T) {
	a := NewMinuteBucketAdmission()
	clock := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	a.clock = func() time.Time { return clock }
	a.window = 10 * time.Millisecond
	_, _ = a.Admit(context.Background(), 9, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notified := make(chan AdmissionResult, 1)
	done := make(chan error, 1)
	go func() {
		_, err := a.AdmitRPMWithWait(ctx, 9, 1, func(result AdmissionResult) {
			notified <- result
		})
		done <- err
	}()
	select {
	case result := <-notified:
		if !result.Waiting || result.Position != 1 {
			t.Fatalf("notification=%+v, want first waiting position", result)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting notification not emitted")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission: %v", err)
	}
}
