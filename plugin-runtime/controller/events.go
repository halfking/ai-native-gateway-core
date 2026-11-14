package controller

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/eventbus"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/ipc"
)

const (
	eventQueueSize         = 32
	maxPluginSubscriptions = 16
	maxSubscriptions       = 128
	maxEventPayload        = 16 * 1024
)

var eventName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// EventRequest is carried by the bounded, complete-JSON IPC protocol. Tokens are
// per-process credentials supplied in the child environment, not gateway keys.
type EventRequest struct {
	Type           string          `json:"type"`
	PluginID       string          `json:"plugin_id"`
	Token          string          `json:"token"`
	Pattern        string          `json:"pattern,omitempty"`
	SubscriptionID string          `json:"subscription_id,omitempty"`
	EventType      string          `json:"event_type,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// WireEvent preserves raw JSON payloads rather than double-encoding JSON bytes.
type WireEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type EventResponse struct {
	Success        bool        `json:"success"`
	Error          string      `json:"error,omitempty"`
	SubscriptionID string      `json:"subscription_id,omitempty"`
	Events         []WireEvent `json:"events,omitempty"`
	Dropped        uint64      `json:"dropped,omitempty"`
}

type eventSubscription struct {
	bus     eventbus.Subscription
	queue   chan *eventbus.Event
	dropped atomic.Uint64
}
type eventIdentity struct {
	token         string
	subscriptions map[string]*eventSubscription
}
type eventTransport struct {
	mu                sync.Mutex
	bus               eventbus.EventBus
	server            *ipc.Server
	socket            string
	identities        map[string]*eventIdentity
	subscriptionCount int
	closed            bool
}

func newEventTransport(ctx context.Context, dir string, bus eventbus.EventBus) (*eventTransport, error) {
	t := &eventTransport{bus: bus, socket: filepath.Join(dir, "events.sock"), identities: make(map[string]*eventIdentity)}
	t.server = ipc.NewServer(t.socket)
	for _, kind := range []string{"event.publish", "event.subscribe", "event.poll", "event.unsubscribe"} {
		t.server.RegisterHandler(kind, t.handle)
	}
	if err := t.server.Start(ctx); err != nil {
		_ = bus.Close()
		return nil, err
	}
	if err := os.Chmod(t.socket, 0600); err != nil {
		_ = t.close()
		return nil, err
	}
	return t, nil
}

func (t *eventTransport) environment(id string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	identity := t.identities[id]
	if identity == nil {
		return nil
	}
	return []string{"GATEWAY_PLUGIN_ID=" + id, "GATEWAY_PLUGIN_EVENT_SOCKET=" + t.socket, "GATEWAY_PLUGIN_EVENT_TOKEN=" + identity.token}
}

func (t *eventTransport) grant(id string) error {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("event transport closed")
	}
	t.revokeLocked(id)
	t.identities[id] = &eventIdentity{token: hex.EncodeToString(token[:]), subscriptions: make(map[string]*eventSubscription)}
	return nil
}

func (t *eventTransport) revoke(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.revokeLocked(id)
}
func (t *eventTransport) revokeLocked(id string) {
	if identity := t.identities[id]; identity != nil {
		for _, sub := range identity.subscriptions {
			_ = sub.bus.Unsubscribe()
			t.subscriptionCount--
		}
		delete(t.identities, id)
	}
}

func (t *eventTransport) handle(ctx context.Context, data []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > maxEventPayload+4096 {
		return nil, errors.New("event request too large")
	}
	var request EventRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	identity := t.identities[request.PluginID]
	if t.closed || identity == nil || subtle.ConstantTimeCompare([]byte(identity.token), []byte(request.Token)) != 1 {
		return nil, errors.New("event identity rejected")
	}
	response := EventResponse{Success: true}
	switch request.Type {
	case "event.publish":
		if !eventName.MatchString(request.EventType) || len(request.Payload) > maxEventPayload || !json.Valid(request.Payload) {
			return nil, errors.New("valid bounded event type and JSON payload required")
		}
		event, err := eventbus.NewEvent(request.EventType, request.PluginID, request.Payload, nil)
		if err != nil {
			return nil, err
		}
		if err := t.bus.Publish(ctx, event); err != nil {
			return nil, err
		}
	case "event.subscribe":
		if !validPattern(request.Pattern) {
			return nil, errors.New("invalid event pattern")
		}
		if len(identity.subscriptions) >= maxPluginSubscriptions || t.subscriptionCount >= maxSubscriptions {
			return nil, errors.New("event subscription limit reached")
		}
		sub := &eventSubscription{queue: make(chan *eventbus.Event, eventQueueSize)}
		handle, err := t.bus.Subscribe(request.Pattern, func(_ context.Context, event *eventbus.Event) error {
			select {
			case sub.queue <- event:
			default:
				sub.dropped.Add(1)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sub.bus = handle
		identity.subscriptions[handle.ID()] = sub
		t.subscriptionCount++
		response.SubscriptionID = handle.ID()
	case "event.poll":
		sub := identity.subscriptions[request.SubscriptionID]
		if sub == nil {
			return nil, errors.New("subscription not owned by plugin")
		}
		for i := 0; i < eventQueueSize; i++ {
			select {
			case event := <-sub.queue:
				response.Events = append(response.Events, WireEvent{ID: event.ID, Type: event.Type, Source: event.Source, Timestamp: event.Timestamp, Payload: event.Payload})
			default:
				i = eventQueueSize
			}
		}
		response.Dropped = sub.dropped.Swap(0)
	case "event.unsubscribe":
		sub := identity.subscriptions[request.SubscriptionID]
		if sub == nil {
			return nil, errors.New("subscription not owned by plugin")
		}
		if err := sub.bus.Unsubscribe(); err != nil {
			return nil, err
		}
		delete(identity.subscriptions, request.SubscriptionID)
		t.subscriptionCount--
	default:
		return nil, fmt.Errorf("unsupported event operation %s", request.Type)
	}
	return json.Marshal(response)
}

func validPattern(pattern string) bool {
	if pattern == "*" {
		return true
	}
	if len(pattern) > 2 && pattern[len(pattern)-2:] == ".*" {
		return eventName.MatchString(pattern[:len(pattern)-2])
	}
	return eventName.MatchString(pattern)
}

func (t *eventTransport) close() error {
	t.mu.Lock()
	t.closed = true
	for id := range t.identities {
		t.revokeLocked(id)
	}
	t.mu.Unlock()
	return errors.Join(t.server.Stop(), t.bus.Close())
}
