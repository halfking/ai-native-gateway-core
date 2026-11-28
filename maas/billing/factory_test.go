package billing

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockEngine 是用于测试的mock计费引擎
type mockEngine struct {
	name string
	mode BillingMode
}

func (m *mockEngine) Name() string {
	return m.name
}

func (m *mockEngine) SupportedMode() BillingMode {
	return m.mode
}

func (m *mockEngine) Calculate(ctx context.Context, consumption *Consumption) (*BillingResult, error) {
	return &BillingResult{
		TotalAmount: 100.0,
		Currency:    "USD",
		EngineName:  m.name,
		BillingMode: m.mode,
	}, nil
}

func (m *mockEngine) Validate(config *BillingConfig) error {
	return nil
}

// TestNewBillingEngineFactory tests factory creation
func TestNewBillingEngineFactory(t *testing.T) {
	factory := NewBillingEngineFactory()
	assert.NotNil(t, factory)
	assert.NotNil(t, factory.engines)
	assert.Equal(t, 0, factory.Count())
}

// TestBillingEngineFactory_Register tests engine registration
func TestBillingEngineFactory_Register(t *testing.T) {
	tests := []struct {
		name    string
		engine  BillingEngine
		wantErr bool
		errMsg  string
	}{
		{
			name:    "register valid engine",
			engine:  &mockEngine{name: "test_engine", mode: ModePayAsYouGo},
			wantErr: false,
		},
		{
			name:    "register nil engine",
			engine:  nil,
			wantErr: true,
			errMsg:  "cannot register nil engine",
		},
		{
			name:    "register engine with empty mode",
			engine:  &mockEngine{name: "bad_engine", mode: ""},
			wantErr: true,
			errMsg:  "has empty billing mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := NewBillingEngineFactory()
			err := factory.Register(tt.engine)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, 1, factory.Count())
			}
		})
	}
}

// TestBillingEngineFactory_RegisterDuplicate tests duplicate registration prevention
func TestBillingEngineFactory_RegisterDuplicate(t *testing.T) {
	factory := NewBillingEngineFactory()

	engine1 := &mockEngine{name: "engine1", mode: ModePayAsYouGo}
	engine2 := &mockEngine{name: "engine2", mode: ModePayAsYouGo}

	// First registration should succeed
	err := factory.Register(engine1)
	assert.NoError(t, err)

	// Second registration with same mode should fail
	err = factory.Register(engine2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")

	// Factory should still have only one engine
	assert.Equal(t, 1, factory.Count())
}

// TestBillingEngineFactory_MustRegister tests panic behavior
func TestBillingEngineFactory_MustRegister(t *testing.T) {
	t.Run("success case", func(t *testing.T) {
		factory := NewBillingEngineFactory()
		engine := &mockEngine{name: "test", mode: ModePayAsYouGo}

		assert.NotPanics(t, func() {
			factory.MustRegister(engine)
		})
	})

	t.Run("panic on nil engine", func(t *testing.T) {
		factory := NewBillingEngineFactory()

		assert.Panics(t, func() {
			factory.MustRegister(nil)
		})
	})

	t.Run("panic on duplicate", func(t *testing.T) {
		factory := NewBillingEngineFactory()
		engine1 := &mockEngine{name: "e1", mode: ModePayAsYouGo}
		engine2 := &mockEngine{name: "e2", mode: ModePayAsYouGo}

		factory.MustRegister(engine1)

		assert.Panics(t, func() {
			factory.MustRegister(engine2)
		})
	})
}

// TestBillingEngineFactory_Create tests engine creation
func TestBillingEngineFactory_Create(t *testing.T) {
	factory := NewBillingEngineFactory()

	engine := &mockEngine{name: "test_engine", mode: ModePayAsYouGo}
	err := factory.Register(engine)
	require.NoError(t, err)

	t.Run("create registered engine", func(t *testing.T) {
		created, err := factory.Create(ModePayAsYouGo)
		assert.NoError(t, err)
		assert.NotNil(t, created)
		assert.Equal(t, "test_engine", created.Name())
		assert.Equal(t, ModePayAsYouGo, created.SupportedMode())
	})

	t.Run("create unregistered engine", func(t *testing.T) {
		created, err := factory.Create(ModeTokenPlan)
		assert.Error(t, err)
		assert.Nil(t, created)
		assert.Contains(t, err.Error(), "no billing engine registered")
	})
}

// TestBillingEngineFactory_Get tests Get alias
func TestBillingEngineFactory_Get(t *testing.T) {
	factory := NewBillingEngineFactory()
	engine := &mockEngine{name: "test", mode: ModePayAsYouGo}
	factory.Register(engine)

	got, err := factory.Get(ModePayAsYouGo)
	assert.NoError(t, err)
	assert.Equal(t, engine, got)
}

// TestBillingEngineFactory_Has tests Has method
func TestBillingEngineFactory_Has(t *testing.T) {
	factory := NewBillingEngineFactory()
	engine := &mockEngine{name: "test", mode: ModePayAsYouGo}
	factory.Register(engine)

	assert.True(t, factory.Has(ModePayAsYouGo))
	assert.False(t, factory.Has(ModeTokenPlan))
	assert.False(t, factory.Has(ModeBoosterPack))
}

// TestBillingEngineFactory_ListModes tests mode listing
func TestBillingEngineFactory_ListModes(t *testing.T) {
	factory := NewBillingEngineFactory()

	// Empty factory
	modes := factory.ListModes()
	assert.Empty(t, modes)

	// Register multiple engines
	factory.Register(&mockEngine{name: "e1", mode: ModePayAsYouGo})
	factory.Register(&mockEngine{name: "e2", mode: ModeTokenPlan})
	factory.Register(&mockEngine{name: "e3", mode: ModeBoosterPack})

	modes = factory.ListModes()
	assert.Len(t, modes, 3)
	assert.Contains(t, modes, ModePayAsYouGo)
	assert.Contains(t, modes, ModeTokenPlan)
	assert.Contains(t, modes, ModeBoosterPack)
}

// TestBillingEngineFactory_ListEngines tests engine listing
func TestBillingEngineFactory_ListEngines(t *testing.T) {
	factory := NewBillingEngineFactory()

	e1 := &mockEngine{name: "engine1", mode: ModePayAsYouGo}
	e2 := &mockEngine{name: "engine2", mode: ModeTokenPlan}

	factory.Register(e1)
	factory.Register(e2)

	engines := factory.ListEngines()
	assert.Len(t, engines, 2)

	names := make([]string, len(engines))
	for i, e := range engines {
		names[i] = e.Name()
	}
	assert.Contains(t, names, "engine1")
	assert.Contains(t, names, "engine2")
}

// TestBillingEngineFactory_Unregister tests engine unregistration
func TestBillingEngineFactory_Unregister(t *testing.T) {
	factory := NewBillingEngineFactory()
	engine := &mockEngine{name: "test", mode: ModePayAsYouGo}
	factory.Register(engine)

	t.Run("unregister existing engine", func(t *testing.T) {
		err := factory.Unregister(ModePayAsYouGo)
		assert.NoError(t, err)
		assert.Equal(t, 0, factory.Count())
		assert.False(t, factory.Has(ModePayAsYouGo))
	})

	t.Run("unregister non-existing engine", func(t *testing.T) {
		err := factory.Unregister(ModeTokenPlan)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not registered")
	})
}

// TestBillingEngineFactory_Reset tests factory reset
func TestBillingEngineFactory_Reset(t *testing.T) {
	factory := NewBillingEngineFactory()

	factory.Register(&mockEngine{name: "e1", mode: ModePayAsYouGo})
	factory.Register(&mockEngine{name: "e2", mode: ModeTokenPlan})
	assert.Equal(t, 2, factory.Count())

	factory.Reset()
	assert.Equal(t, 0, factory.Count())
	assert.False(t, factory.Has(ModePayAsYouGo))
	assert.False(t, factory.Has(ModeTokenPlan))
}

// TestBillingEngineFactory_ConcurrentAccess tests thread safety
func TestBillingEngineFactory_ConcurrentAccess(t *testing.T) {
	factory := NewBillingEngineFactory()

	// Pre-register one engine
	factory.Register(&mockEngine{name: "initial", mode: ModePayAsYouGo})

	var wg sync.WaitGroup
	concurrency := 100

	// Concurrent reads
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			factory.Has(ModePayAsYouGo)
			factory.ListModes()
			factory.Create(ModePayAsYouGo)
		}()
	}

	// Concurrent writes (different modes to avoid duplicate errors)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			mode := BillingMode(fmt.Sprintf("mode_%d", idx))
			engine := &mockEngine{name: fmt.Sprintf("engine_%d", idx), mode: mode}
			factory.Register(engine)
		}(i)
	}

	wg.Wait()

	// Verify factory is still consistent
	assert.GreaterOrEqual(t, factory.Count(), 1) // At least the initial engine
}

// TestDefaultFactory tests global default factory
func TestDefaultFactory(t *testing.T) {
	// Save original state
	originalEngines := DefaultFactory.ListEngines()

	// Reset for clean test
	DefaultFactory.Reset()
	defer func() {
		// Restore original state
		DefaultFactory.Reset()
		for _, e := range originalEngines {
			DefaultFactory.Register(e)
		}
	}()

	// Test global convenience functions
	engine := &mockEngine{name: "global_test", mode: "test_mode"}

	err := Register(engine)
	assert.NoError(t, err)

	assert.True(t, Has("test_mode"))

	created, err := Get("test_mode")
	assert.NoError(t, err)
	assert.Equal(t, engine, created)

	modes := ListModes()
	assert.Contains(t, modes, BillingMode("test_mode"))
}

// TestBillingEngineFactory_Count tests count method
func TestBillingEngineFactory_Count(t *testing.T) {
	factory := NewBillingEngineFactory()
	assert.Equal(t, 0, factory.Count())

	factory.Register(&mockEngine{name: "e1", mode: ModePayAsYouGo})
	assert.Equal(t, 1, factory.Count())

	factory.Register(&mockEngine{name: "e2", mode: ModeTokenPlan})
	assert.Equal(t, 2, factory.Count())

	factory.Unregister(ModePayAsYouGo)
	assert.Equal(t, 1, factory.Count())

	factory.Reset()
	assert.Equal(t, 0, factory.Count())
}
