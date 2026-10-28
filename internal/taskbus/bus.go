package taskbus

import (
	"context"
	"fmt"
)

// Bus 组合 Store + Registry + Dispatcher，实现 Enqueuer。
type Bus struct {
	Store      Store
	Registry   *Registry
	Dispatcher *Dispatcher
}

// NewBus 组装总线；registry/store 不可空。
func NewBus(store Store, registry *Registry) *Bus {
	if store == nil {
		store = NewMemoryStore()
	}
	if registry == nil {
		registry = NewRegistry()
	}
	d := NewDispatcher(DispatcherConfig{Store: store, Registry: registry})
	return &Bus{Store: store, Registry: registry, Dispatcher: d}
}

// NewDemoBus 本波示范：MemoryStore + ASM 租户登记（cache handler 由调用方再挂）。
func NewDemoBus() *Bus {
	r := NewRegistry()
	RegisterASMTenant(r)
	return NewBus(NewMemoryStore(), r)
}

// Enqueue 实现 Enqueuer。
func (b *Bus) Enqueue(ctx context.Context, taskType, idempotencyKey string, schemaVersion int, payload []byte) error {
	if b == nil || b.Store == nil {
		return fmt.Errorf("taskbus.Bus.Enqueue: nil bus/store")
	}
	_, err := b.Store.Enqueue(ctx, taskType, idempotencyKey, schemaVersion, payload)
	return err
}

// HasHandler 透传 Registry（供 pipeline.SetHandlerLookup）。
func (b *Bus) HasHandler(taskType string) bool {
	if b == nil || b.Registry == nil {
		return false
	}
	return b.Registry.HasHandler(taskType)
}

// DispatchOnce 透传 Dispatcher。
func (b *Bus) DispatchOnce(ctx context.Context) (int, error) {
	if b == nil || b.Dispatcher == nil {
		return 0, fmt.Errorf("taskbus.Bus: nil dispatcher")
	}
	return b.Dispatcher.DispatchOnce(ctx)
}
