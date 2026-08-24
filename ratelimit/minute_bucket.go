package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrMinuteBucketFull = errors.New("rate limit queue full")
var ErrMinuteBucketWaitTimeout = errors.New("rate limit queue wait timeout")

const maxMinuteBucketWait = 2 * time.Minute

type AdmissionResult struct {
	Admitted       bool
	Waiting        bool
	Position       int
	Limit          int
	Remaining      int
	QueueRemaining int
}

type minuteBucket struct {
	count int
	start time.Time
}

type queuedRequest struct {
	ready  chan struct{}
	queued bool
}

type MinuteBucketAdmission struct {
	mu      sync.Mutex
	clock   func() time.Time
	window  time.Duration
	current map[int]*minuteBucket
	queues  map[int][]*queuedRequest
}

func NewMinuteBucketAdmission() *MinuteBucketAdmission {
	return &MinuteBucketAdmission{
		clock:   time.Now,
		window:  time.Minute,
		current: make(map[int]*minuteBucket),
		queues:  make(map[int][]*queuedRequest),
	}
}

func (a *MinuteBucketAdmission) Admit(ctx context.Context, keyID, limit int) (AdmissionResult, error) {
	return a.admit(ctx, keyID, limit, nil)
}

func (a *MinuteBucketAdmission) AdmitRPMWithWait(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error) {
	return a.admit(ctx, keyID, limit, notify)
}

func (a *MinuteBucketAdmission) admit(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error) {
	if limit <= 0 {
		return AdmissionResult{Admitted: true}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	a.mu.Lock()
	now := a.clock()
	bucket := a.current[keyID]
	if bucket == nil || !sameWindow(bucket.start, now, a.window) {
		bucket = &minuteBucket{start: bucketStart(now, a.window)}
		a.current[keyID] = bucket
		a.releaseWaitersLocked(keyID, limit)
	}
	queue := a.queues[keyID]
	if len(queue) == 0 && bucket.count < limit {
		bucket.count++
		result := AdmissionResult{Admitted: true, Limit: limit, Remaining: limit - bucket.count}
		a.mu.Unlock()
		return result, nil
	}
	if len(queue) >= limit {
		a.mu.Unlock()
		return AdmissionResult{Limit: limit}, ErrMinuteBucketFull
	}
	waiter := &queuedRequest{ready: make(chan struct{}), queued: true}
	a.queues[keyID] = append(queue, waiter)
	result := AdmissionResult{Waiting: true, Position: len(queue) + 1, Limit: limit}
	result.QueueRemaining = limit - len(queue) - 1
	a.mu.Unlock()
	if notify != nil {
		notify(result)
	}

	for {
		wait := a.timeUntilNextBucket()
		if wait > maxMinuteBucketWait {
			wait = maxMinuteBucketWait
		}
		timer := time.NewTimer(wait)
		select {
		case <-waiter.ready:
			timer.Stop()
			return AdmissionResult{Admitted: true, Limit: limit}, nil
		case <-ctx.Done():
			timer.Stop()
			a.cancel(keyID, waiter)
			return AdmissionResult{Waiting: true, Position: result.Position, Limit: limit}, ctx.Err()
		case <-timer.C:
			if wait == maxMinuteBucketWait {
				a.cancel(keyID, waiter)
				return result, ErrMinuteBucketWaitTimeout
			}
			a.mu.Lock()
			if waiter.queued {
				now := a.clock()
				bucket := a.current[keyID]
				if bucket == nil || !sameWindow(bucket.start, now, a.window) {
					bucket = &minuteBucket{start: bucketStart(now, a.window)}
					a.current[keyID] = bucket
					a.releaseWaitersLocked(keyID, limit)
				}
			}
			a.mu.Unlock()
		}
	}
}

func (a *MinuteBucketAdmission) AdmitRPM(ctx context.Context, keyID, limit int) (AdmissionResult, error) {
	return a.Admit(ctx, keyID, limit)
}

func (a *MinuteBucketAdmission) releaseWaitersLocked(keyID, limit int) {
	bucket := a.current[keyID]
	queue := a.queues[keyID]
	for bucket.count < limit && len(queue) > 0 {
		waiter := queue[0]
		queue = queue[1:]
		waiter.queued = false
		bucket.count++
		close(waiter.ready)
	}
	if len(queue) == 0 {
		delete(a.queues, keyID)
	} else {
		a.queues[keyID] = queue
	}
}

func (a *MinuteBucketAdmission) cancel(keyID int, waiter *queuedRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !waiter.queued {
		return
	}
	queue := a.queues[keyID]
	for i, item := range queue {
		if item == waiter {
			a.queues[keyID] = append(queue[:i], queue[i+1:]...)
			waiter.queued = false
			return
		}
	}
}

func (a *MinuteBucketAdmission) timeUntilNextBucket() time.Duration {
	now := a.clock()
	elapsed := now.Sub(bucketStart(now, a.window))
	remaining := a.window - elapsed
	if remaining <= 0 {
		return time.Millisecond
	}
	return remaining
}

func bucketStart(t time.Time, window time.Duration) time.Time {
	u := t.UTC()
	return time.Unix(0, u.UnixNano()/window.Nanoseconds()*window.Nanoseconds()).UTC()
}

func sameWindow(a, b time.Time, window time.Duration) bool {
	return bucketStart(a, window).Equal(bucketStart(b, window))
}
