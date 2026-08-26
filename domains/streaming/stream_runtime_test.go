package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/config"
)

func TestCurrentStreamRuntimeConfigEarlyEmptyDefaultsAndEnv(t *testing.T) {
	previous := streamConfigStore.Load()
	t.Cleanup(func() { streamConfigStore.Store(previous) })

	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "")
	streamConfigStore.Store(config.NewStore(&config.Config{EnableEmptyStreamGate: true}))
	if got := currentStreamRuntimeConfig().emptyStreamEarlyEmptyChunks; got != 3 {
		t.Fatalf("default early-empty threshold = %d, want 3", got)
	}

	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "5")
	if got := currentStreamRuntimeConfig().emptyStreamEarlyEmptyChunks; got != 5 {
		t.Fatalf("env early-empty threshold = %d, want 5", got)
	}

	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "0")
	if got := currentStreamRuntimeConfig().emptyStreamEarlyEmptyChunks; got != 0 {
		t.Fatalf("zero early-empty threshold = %d, want disabled", got)
	}

	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "invalid")
	if got := currentStreamRuntimeConfig().emptyStreamEarlyEmptyChunks; got != 3 {
		t.Fatalf("invalid early-empty threshold = %d, want default 3", got)
	}
}

func TestCurrentStreamRuntimeConfigEarlyEmptyStoreValue(t *testing.T) {
	previous := streamConfigStore.Load()
	t.Cleanup(func() { streamConfigStore.Store(previous) })
	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "")
	streamConfigStore.Store(config.NewStore(&config.Config{
		EnableEmptyStreamGate:       true,
		EmptyStreamEarlyEmptyChunks: 7,
	}))
	if got := currentStreamRuntimeConfig().emptyStreamEarlyEmptyChunks; got != 7 {
		t.Fatalf("store early-empty threshold = %d, want 7", got)
	}
}
