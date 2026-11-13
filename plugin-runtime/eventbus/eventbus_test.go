package eventbus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Test basic publish-subscribe functionality
func TestPublishSubscribe(t *testing.T) {
	bus := NewMemoryEventBus(10)
	defer bus.Close()

	received := make(chan *Event, 1)

	// Subscribe to plugin events
	handler := func(ctx context.Context, event *Event) error {
		received <- event
		return nil
	}

	sub, err := bus.Subscribe("plugin.started", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	// Publish event
	event := &Event{
		ID:        "test-1",
		Type:      "plugin.started",
		Source:    "test",
		Timestamp: time.Now(),
		Payload:   []byte(`{"plugin":"test-plugin"}`),
	}

	ctx := context.Background()
	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Wait for event
	select {
	case recv := <-received:
		if recv.ID != event.ID {
			t.Errorf("Expected event ID %s, got %s", event.ID, recv.ID)
		}
		if recv.Type != event.Type {
			t.Errorf("Expected event type %s, got %s", event.Type, recv.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for event")
	}
}

// Test wildcard subscription
func TestWildcardSubscription(t *testing.T) {
	bus := NewMemoryEventBus(10)
	defer bus.Close()

	var mu sync.Mutex
	receivedTypes := make(map[string]int)

	// Subscribe to all plugin events
	handler := func(ctx context.Context, event *Event) error {
		mu.Lock()
		receivedTypes[event.Type]++
		mu.Unlock()
		return nil
	}

	sub, err := bus.Subscribe("plugin.*", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	// Publish multiple plugin events
	ctx := context.Background()
	events := []string{
		"plugin.started",
		"plugin.stopped",
		"plugin.crashed",
	}

	for _, eventType := range events {
		event := &Event{
			ID:        "test-" + eventType,
			Type:      eventType,
			Source:    "test",
			Timestamp: time.Now(),
		}
		if err := bus.Publish(ctx, event); err != nil {
			t.Fatalf("Publish failed: %v", err)
		}
	}

	// Wait for events to be processed
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Check all events were received
	for _, eventType := range events {
		if count := receivedTypes[eventType]; count != 1 {
			t.Errorf("Expected 1 event of type %s, got %d", eventType, count)
		}
	}
}

// Test multiple subscribers
func TestMultipleSubscribers(t *testing.T) {
	bus := NewMemoryEventBus(10)
	defer bus.Close()

	var count1, count2 int32

	// First subscriber
	handler1 := func(ctx context.Context, event *Event) error {
		atomic.AddInt32(&count1, 1)
		return nil
	}

	sub1, err := bus.Subscribe("plugin.started", handler1)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub1.Unsubscribe()

	// Second subscriber
	handler2 := func(ctx context.Context, event *Event) error {
		atomic.AddInt32(&count2, 1)
		return nil
	}

	sub2, err := bus.Subscribe("plugin.started", handler2)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub2.Unsubscribe()

	// Publish event
	ctx := context.Background()
	event := &Event{
		ID:        "test-1",
		Type:      "plugin.started",
		Source:    "test",
		Timestamp: time.Now(),
	}

	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Both subscribers should receive the event
	if c := atomic.LoadInt32(&count1); c != 1 {
		t.Errorf("Expected subscriber 1 to receive 1 event, got %d", c)
	}
	if c := atomic.LoadInt32(&count2); c != 1 {
		t.Errorf("Expected subscriber 2 to receive 1 event, got %d", c)
	}
}

// Test non-blocking publish
func TestNonBlockingPublish(t *testing.T) {
	bus := NewMemoryEventBus(2) // Small buffer
	defer bus.Close()

	// Slow handler that blocks
	blockCh := make(chan struct{})
	handler := func(ctx context.Context, event *Event) error {
		<-blockCh // Block until released
		return nil
	}

	sub, err := bus.Subscribe("plugin.started", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	ctx := context.Background()

	// Publish events rapidly (more than buffer size)
	start := time.Now()
	for i := 0; i < 10; i++ {
		event := &Event{
			ID:        fmt.Sprintf("test-%d", i),
			Type:      "plugin.started",
			Source:    "test",
			Timestamp: time.Now(),
		}
		if err := bus.Publish(ctx, event); err != nil {
			t.Fatalf("Publish failed: %v", err)
		}
	}
	elapsed := time.Since(start)

	// Publishing should be fast (non-blocking)
	if elapsed > 10*time.Millisecond {
		t.Errorf("Publish took too long: %v (expected < 10ms)", elapsed)
	}

	// Release handler
	close(blockCh)
}

// Test unsubscribe
func TestUnsubscribe(t *testing.T) {
	bus := NewMemoryEventBus(10)
	defer bus.Close()

	var count int32

	handler := func(ctx context.Context, event *Event) error {
		atomic.AddInt32(&count, 1)
		return nil
	}

	sub, err := bus.Subscribe("plugin.started", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ctx := context.Background()

	// Publish first event
	event1 := &Event{
		ID:        "test-1",
		Type:      "plugin.started",
		Source:    "test",
		Timestamp: time.Now(),
	}
	if err := bus.Publish(ctx, event1); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Unsubscribe
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	// Publish second event (should not be received)
	event2 := &Event{
		ID:        "test-2",
		Type:      "plugin.started",
		Source:    "test",
		Timestamp: time.Now(),
	}
	if err := bus.Publish(ctx, event2); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Should only have received first event
	if c := atomic.LoadInt32(&count); c != 1 {
		t.Errorf("Expected 1 event after unsubscribe, got %d", c)
	}
}

// Test event bus close
func TestEventBusClose(t *testing.T) {
	bus := NewMemoryEventBus(10)

	// Subscribe
	handler := func(ctx context.Context, event *Event) error {
		return nil
	}

	sub, err := bus.Subscribe("plugin.started", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Close bus
	if err := bus.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Operations should fail after close
	ctx := context.Background()
	event := &Event{
		ID:        "test-1",
		Type:      "plugin.started",
		Source:    "test",
		Timestamp: time.Now(),
	}

	err = bus.Publish(ctx, event)
	if err != ErrEventBusClosed {
		t.Errorf("Expected ErrEventBusClosed, got %v", err)
	}

	_, err = bus.Subscribe("plugin.*", handler)
	if err != ErrEventBusClosed {
		t.Errorf("Expected ErrEventBusClosed on subscribe, got %v", err)
	}

	// Unsubscribe should still work (idempotent)
	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("Unsubscribe after close failed: %v", err)
	}
}

// Test pattern matching
func TestPatternMatching(t *testing.T) {
	tests := []struct {
		pattern   string
		eventType string
		matches   bool
	}{
		{"plugin.started", "plugin.started", true},
		{"plugin.started", "plugin.stopped", false},
		{"plugin.*", "plugin.started", true},
		{"plugin.*", "plugin.stopped", true},
		{"plugin.*", "maas.inventory.changed", false},
		{"*", "plugin.started", true},
		{"*", "maas.inventory.changed", true},
		{"maas.*", "maas.inventory.changed", true}, // Matches: starts with "maas."
		{"maas.inventory.*", "maas.inventory.changed", true},
		{"plugin.*", "pluginstarted", false}, // No match: missing dot
	}

	for _, tt := range tests {
		result := matchPattern(tt.pattern, tt.eventType)
		if result != tt.matches {
			t.Errorf("matchPattern(%q, %q) = %v, want %v",
				tt.pattern, tt.eventType, result, tt.matches)
		}
	}
}

// Test subscription ID
func TestSubscriptionID(t *testing.T) {
	bus := NewMemoryEventBus(10)
	defer bus.Close()

	handler := func(ctx context.Context, event *Event) error {
		return nil
	}

	sub1, err := bus.Subscribe("plugin.*", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub1.Unsubscribe()

	sub2, err := bus.Subscribe("plugin.*", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub2.Unsubscribe()

	// IDs should be different
	if sub1.ID() == sub2.ID() {
		t.Error("Expected different subscription IDs")
	}

	// Pattern should match
	if sub1.Pattern() != "plugin.*" {
		t.Errorf("Expected pattern 'plugin.*', got %s", sub1.Pattern())
	}
}

// Test event helper functions
func TestNewPluginEvent(t *testing.T) {
	payload := map[string]string{"name": "test-plugin"}
	event, err := NewPluginEvent(EventTypePluginStarted, "test-plugin-id", payload)
	if err != nil {
		t.Fatalf("NewPluginEvent failed: %v", err)
	}

	if event.Type != EventTypePluginStarted {
		t.Errorf("Expected type %s, got %s", EventTypePluginStarted, event.Type)
	}

	if event.Source != "plugin-runtime" {
		t.Errorf("Expected source 'plugin-runtime', got %s", event.Source)
	}

	if event.Metadata["plugin_id"] != "test-plugin-id" {
		t.Errorf("Expected plugin_id 'test-plugin-id', got %s", event.Metadata["plugin_id"])
	}

	if len(event.ID) == 0 {
		t.Error("Expected non-empty event ID")
	}
}

func TestNewMaaSEvent(t *testing.T) {
	payload := map[string]interface{}{"quantity": 100}
	event, err := NewMaaSEvent(EventTypeMaaSInventorySellableChanged, "inventory-service", payload)
	if err != nil {
		t.Fatalf("NewMaaSEvent failed: %v", err)
	}

	if event.Type != EventTypeMaaSInventorySellableChanged {
		t.Errorf("Expected type %s, got %s", EventTypeMaaSInventorySellableChanged, event.Type)
	}

	if event.Source != "inventory-service" {
		t.Errorf("Expected source 'inventory-service', got %s", event.Source)
	}
}

// Test concurrent publish
func TestConcurrentPublish(t *testing.T) {
	bus := NewMemoryEventBus(100)
	defer bus.Close()

	var received int32

	handler := func(ctx context.Context, event *Event) error {
		atomic.AddInt32(&received, 1)
		return nil
	}

	sub, err := bus.Subscribe("test.*", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	// Publish events concurrently
	ctx := context.Background()
	var wg sync.WaitGroup
	numGoroutines := 10
	eventsPerGoroutine := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				event := &Event{
					ID:        fmt.Sprintf("test-%d-%d", id, j),
					Type:      "test.event",
					Source:    "test",
					Timestamp: time.Now(),
				}
				if err := bus.Publish(ctx, event); err != nil {
					t.Errorf("Publish failed: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Wait for all events to be processed
	time.Sleep(200 * time.Millisecond)

	expected := int32(numGoroutines * eventsPerGoroutine)
	if count := atomic.LoadInt32(&received); count != expected {
		t.Errorf("Expected %d events, got %d", expected, count)
	}
}

// Benchmark publish performance
func BenchmarkPublish(b *testing.B) {
	bus := NewMemoryEventBus(1000)
	defer bus.Close()

	handler := func(ctx context.Context, event *Event) error {
		return nil
	}

	bus.Subscribe("test.*", handler)

	ctx := context.Background()
	event := &Event{
		ID:        "bench-1",
		Type:      "test.event",
		Source:    "benchmark",
		Timestamp: time.Now(),
		Payload:   []byte(`{"test":"data"}`),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(ctx, event)
	}
}
