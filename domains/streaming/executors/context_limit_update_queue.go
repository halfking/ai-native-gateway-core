package executors

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

type contextLimitUpdate struct {
	providerID   int
	credentialID int
	rawModel     string
	limit        int
}

// ContextLimitUpdateQueue owns a bounded worker for discovered-limit writes.
// Enqueue is non-blocking; Stop closes admission, drains queued work, and waits
// for the worker before the shared database pool is closed.
type ContextLimitUpdateQueue struct {
	updater ContextLimitUpdater
	queue   chan contextLimitUpdate
	done    chan struct{}
	stop    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	closed  bool
}

func NewContextLimitUpdateQueue(updater ContextLimitUpdater, capacity int) *ContextLimitUpdateQueue {
	if capacity < 1 {
		capacity = 64
	}
	q := &ContextLimitUpdateQueue{
		updater: updater,
		queue:   make(chan contextLimitUpdate, capacity),
		done:    make(chan struct{}),
		stop:    make(chan struct{}),
	}
	go q.run()
	return q
}

func (q *ContextLimitUpdateQueue) process(item contextLimitUpdate) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	updated, err := q.updater.UpdateContextLimit(ctx, item.credentialID, item.rawModel, item.limit)
	cancel()
	model := normalizeContextLimitMetricModel(item.rawModel)
	provider := strconv.Itoa(item.providerID)
	if item.providerID <= 0 {
		provider = "unknown"
	}
	if err != nil {
		contextLimitDiscoveryTotal.WithLabelValues(provider, model, "failed").Inc()
	} else if updated {
		contextLimitDiscoveryTotal.WithLabelValues(provider, model, "persisted").Inc()
	} else {
		contextLimitDiscoveryTotal.WithLabelValues(provider, model, "skipped").Inc()
	}
}

func (q *ContextLimitUpdateQueue) run() {
	defer close(q.done)
	for {
		select {
		case item := <-q.queue:
			q.process(item)
		case <-q.stop:
			for {
				select {
				case item := <-q.queue:
					q.process(item)
				default:
					return
				}
			}
		}
	}
}

func (q *ContextLimitUpdateQueue) Enqueue(providerID, credentialID int, rawModel string, limit int) bool {
	if q == nil || q.updater == nil || limit <= 0 || strings.TrimSpace(rawModel) == "" {
		return false
	}
	item := contextLimitUpdate{providerID: providerID, credentialID: credentialID, rawModel: strings.TrimSpace(rawModel), limit: limit}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	select {
	case q.queue <- item:
		return true
	default:
		provider := "unknown"
		if providerID > 0 {
			provider = strconv.Itoa(providerID)
		}
		contextLimitDiscoveryTotal.WithLabelValues(provider, normalizeContextLimitMetricModel(item.rawModel), "dropped").Inc()
		return false
	}
}

func (q *ContextLimitUpdateQueue) Stop() {
	if q == nil {
		return
	}
	q.once.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		close(q.stop)
	})
	<-q.done
}

func normalizeContextLimitMetricModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	if len(model) > 96 {
		return model[:95] + "..."
	}
	return model
}
