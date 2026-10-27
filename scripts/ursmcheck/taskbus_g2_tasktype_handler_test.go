package ursm_test

// taskbus_g2_tasktype_handler_test.go — G2 任务类型↔消费者。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"
	"github.com/kaixuan/llm-gateway-go/internal/taskbus"
)

func TestTaskBusG2_EveryDemoTaskTypeHasHandler(t *testing.T) {
	bus := taskbus.NewDemoBus()
	store := cache.NewInMemoryStore()
	cache.RegisterCacheSaveHandler(bus.Registry, store)

	required := []string{
		taskbus.TaskTypeASMEventV1,
		taskbus.TaskTypeCacheSaveV1,
	}
	for _, tt := range required {
		if !bus.HasHandler(tt) {
			t.Fatalf("G2: TaskType %q has no handler", tt)
		}
	}
}
