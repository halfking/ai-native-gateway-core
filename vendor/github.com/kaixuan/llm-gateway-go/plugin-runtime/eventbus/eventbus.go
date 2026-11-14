// Package eventbus implements a publish-subscribe event bus for plugin communication.
//
// EventBus enables loose coupling between plugins through asynchronous event messaging.
// It supports wildcard subscriptions and non-blocking event delivery.
package eventbus

import (
	"context"
	"time"
)

// EventBus defines the event bus interface for publish/subscribe messaging.
type EventBus interface {
	// Publish publishes an event to all matching subscribers.
	// Returns immediately without waiting for subscribers to process the event.
	Publish(ctx context.Context, event *Event) error

	// Subscribe subscribes to events matching the pattern.
	// Pattern supports wildcards: "plugin.*" matches all plugin events.
	// The handler is called asynchronously for each matching event.
	Subscribe(pattern string, handler EventHandler) (Subscription, error)

	// Unsubscribe removes a subscription.
	Unsubscribe(sub Subscription) error

	// Close closes the event bus and releases all resources.
	// All active subscriptions are cancelled.
	Close() error
}

// Event represents a domain event in the system.
type Event struct {
	// ID is a unique identifier for this event.
	ID string

	// Type identifies the kind of event (e.g., "plugin.started").
	Type string

	// Source identifies where the event originated (e.g., "supervisor").
	Source string

	// Timestamp is when the event occurred.
	Timestamp time.Time

	// Payload contains the event data as JSON.
	Payload []byte

	// Metadata contains optional key-value metadata.
	Metadata map[string]string
}

// EventHandler is called when a matching event is received.
// The handler should return quickly to avoid blocking event delivery.
// Long-running operations should be handled asynchronously.
type EventHandler func(ctx context.Context, event *Event) error

// Subscription represents an active event subscription.
type Subscription interface {
	// ID returns the unique subscription identifier.
	ID() string

	// Pattern returns the event pattern this subscription matches.
	Pattern() string

	// Unsubscribe removes this subscription from the event bus.
	Unsubscribe() error
}
