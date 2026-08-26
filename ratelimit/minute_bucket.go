package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrMinuteBucketFull = errors.New("rate limit queue full")
var ErrMinuteBucketWaitTimeout = errors.New("rate limit queue wait timeout")
var ErrQueueBudgetExceeded = errors.New("rate limit queue budget exceeded")

const maxMinuteBucketWait = 2 * time.Minute

type AdmissionResult struct {
	Admitted       bool
	Waiting        bool
	Position       int
	Limit          int
	Remaining      int
	QueueRemaining int
	// EstimatedWaitSec is the server's estimate of queue wait time in
	// seconds, populated when the request is queued or rejected for budget
	// reasons. Used to write Retry-After on the 429 fast-reject path.
	EstimatedWaitSec int
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
	return a.admit(ctx, keyID, limit, 0, nil)
}

func (a *MinuteBucketAdmission) AdmitRPMWithWait(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error) {
	return a.admit(ctx, keyID, limit, 0, notify)
}

// AdmitRPMWithBudget is AdmitRPMWithWait with a hard wait cap. When queued,
// the estimated wait (buckets-ahead × window + time to next bucket) is
// compared against maxWait; if it exceeds the caller's remaining request
// budget the request is rejected fast WITHOUT being enqueued, so it does not
// consume its whole timeout budget waiting and then die with a timeout.
func (a *MinuteBucketAdmission) AdmitRPMWithBudget(ctx context.Context, keyID, limit int, maxWait time.Duration, notify func(AdmissionResult)) (AdmissionResult, error) {
	return a.admit(ctx, keyID, limit, maxWait, notify)
}

// estimateWaitLocked computes (under a.mu) how long a fresh queue waiter at
// `position` would wait: time until the next bucket boundary plus full
// windows for the queue slots ahead (each window admits at most `limit`).
func (a *MinuteBucketAdmission) estimateWaitLocked(position, limit int) time.Duration {
	if position <= 1 {
		return a.timeUntilNextBucket()
	}
	windowsAhead := (position - 1 + limit - 1) / limit
	return a.timeUntilNextBucket() + time.Duration(windowsAhead-1)*a.window
}

func (a *MinuteBucketAdmission) admit(ctx context.Context, keyID, limit int, maxWait time.Duration, notify func(AdmissionResult)) (AdmissionResult, error) {
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
	position := len(queue) + 1
	estimatedWait := a.estimateWaitLocked(position, limit)
	if maxWait > 0 && estimatedWait > maxWait {
		// Fail fast: this request cannot be admitted within the caller's
		// remaining budget. Do NOT enqueue — otherwise the request would
		// burn its entire timeout in the queue and die as a 502/timeout
		// with no upstream attempt logged (2026-08-26 kimi-k3 incident).
		a.mu.Unlock()
		return AdmissionResult{
			Limit:            limit,
			Position:         position,
			QueueRemaining:   limit - len(queue) - 1,
			EstimatedWaitSec: int(estimatedWait.Round(time.Second) / time.Second),
		}, ErrQueueBudgetExceeded
	}
	waiter := &queuedRequest{ready: make(chan struct{}), queued: true}
	a.queues[keyID] = append(queue, waiter)
	result := AdmissionResult{Waiting: true, Position: position, Limit: limit}
	result.QueueRemaining = limit - len(queue) - 1
	result.EstimatedWaitSec = int(estimatedWait.Round(time.Second) / time.Second)
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
