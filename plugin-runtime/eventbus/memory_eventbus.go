package eventbus

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
)

// memoryEventBus is an in-memory implementation of EventBus.
type memoryEventBus struct {
	mu            sync.RWMutex
	subscriptions map[string]*subscription
	nextSubID     int64
	bufferSize    int
	closed        bool
}

// subscription represents an active subscription.
type subscription struct {
	id      string
	pattern string
	handler EventHandler
	ch      chan *Event
	cancel  context.CancelFunc
	bus     *memoryEventBus
	ctx     context.Context
}

// NewMemoryEventBus creates an in-memory event bus.
// bufferSize determines the channel buffer size for each subscription.
// If bufferSize <= 0, defaults to 100.
func NewMemoryEventBus(bufferSize int) EventBus {
	if bufferSize <= 0 {
		bufferSize = 100
	}

	return &memoryEventBus{
		subscriptions: make(map[string]*subscription),
		bufferSize:    bufferSize,
	}
}

// Publish publishes an event to all matching subscribers.
func (bus *memoryEventBus) Publish(ctx context.Context, event *Event) error {
	bus.mu.RLock()
	defer bus.mu.RUnlock()

	if bus.closed {
		return ErrEventBusClosed
	}

	// Find matching subscriptions
	for _, sub := range bus.subscriptions {
		if matchPattern(sub.pattern, event.Type) {
			// Non-blocking send
			select {
			case sub.ch <- event:
				// Event sent successfully
			default:
				// Channel full, drop event with warning
				log.Printf("WARN: Event %s dropped for subscription %s (channel full)", event.ID, sub.id)
			}
		}
	}

	return nil
}

// Subscribe subscribes to events matching the pattern.
func (bus *memoryEventBus) Subscribe(pattern string, handler EventHandler) (Subscription, error) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	if bus.closed {
		return nil, ErrEventBusClosed
	}

	// Generate subscription ID
	bus.nextSubID++
	subID := fmt.Sprintf("sub-%d", bus.nextSubID)

	// Create subscription context
	ctx, cancel := context.WithCancel(context.Background())

	// Create subscription
	sub := &subscription{
		id:      subID,
		pattern: pattern,
		handler: handler,
		ch:      make(chan *Event, bus.bufferSize),
		cancel:  cancel,
		bus:     bus,
		ctx:     ctx,
	}

	bus.subscriptions[subID] = sub

	// Start event handler goroutine
	go sub.run()

	return sub, nil
}

// Unsubscribe removes a subscription.
func (bus *memoryEventBus) Unsubscribe(sub Subscription) error {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	if s, exists := bus.subscriptions[sub.ID()]; exists {
		s.cancel()
		close(s.ch)
		delete(bus.subscriptions, sub.ID())
	}

	return nil
}

// Close closes the event bus and releases all resources.
func (bus *memoryEventBus) Close() error {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	if bus.closed {
		return nil
	}

	bus.closed = true

	// Cancel all subscriptions
	for _, sub := range bus.subscriptions {
		sub.cancel()
		close(sub.ch)
	}

	bus.subscriptions = make(map[string]*subscription)

	return nil
}

// subscription implementation

// ID returns the subscription ID.
func (s *subscription) ID() string {
	return s.id
}

// Pattern returns the subscription pattern.
func (s *subscription) Pattern() string {
	return s.pattern
}

// Unsubscribe removes this subscription.
func (s *subscription) Unsubscribe() error {
	return s.bus.Unsubscribe(s)
}

// run processes events for this subscription.
func (s *subscription) run() {
	for {
		select {
		case event, ok := <-s.ch:
			if !ok {
				// Channel closed
				return
			}

			// Call handler with subscription context
			if err := s.handler(s.ctx, event); err != nil {
				log.Printf("ERROR: Event handler failed for subscription %s: %v", s.id, err)
			}

		case <-s.ctx.Done():
			// Subscription cancelled
			return
		}
	}
}

// matchPattern checks if an event type matches a subscription pattern.
// Supports wildcard patterns: "plugin.*" matches "plugin.started", "plugin.stopped", etc.
func matchPattern(pattern, eventType string) bool {
	// Exact match
	if pattern == eventType {
		return true
	}

	// Wildcard match: "plugin.*" matches "plugin.started"
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		return strings.HasPrefix(eventType, prefix+".")
	}

	// Match all: "*"
	if pattern == "*" {
		return true
	}

	return false
}

// Errors

var (
	// ErrEventBusClosed is returned when operating on a closed event bus.
	ErrEventBusClosed = errors.New("event bus is closed")
)
