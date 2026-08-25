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

func TestModelAliasPrefixDefault(t *testing.T) {
	previous := streamConfigStore.Load()
	t.Cleanup(func() { streamConfigStore.Store(previous) })
	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "")
	streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "kx-"}))
	if got := ModelAliasPrefix(); got != "kx-" {
		t.Fatalf("default alias prefix = %q, want %q", got, "kx-")
	}
}

func TestModelAliasPrefixFromStore(t *testing.T) {
	previous := streamConfigStore.Load()
	t.Cleanup(func() { streamConfigStore.Store(previous) })
	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "")

	streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "kx-"}))
	if got := ModelAliasPrefix(); got != "kx-" {
		t.Fatalf("store prefix = %q, want kx-", got)
	}

	streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "custom-"}))
	if got := ModelAliasPrefix(); got != "custom-" {
		t.Fatalf("custom store prefix = %q, want custom-", got)
	}

	streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: ""}))
	if got := ModelAliasPrefix(); got != "" {
		t.Fatalf("empty store prefix = %q, want empty", got)
	}
}

func TestModelAliasPrefixFallback(t *testing.T) {
	previous := streamConfigStore.Load()
	t.Cleanup(func() { streamConfigStore.Store(previous) })

	streamConfigStore.Store(nil)

	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "")
	if got := ModelAliasPrefix(); got != "kx-" {
		t.Fatalf("default fallback prefix = %q, want kx-", got)
	}

	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "env-")
	if got := ModelAliasPrefix(); got != "env-" {
		t.Fatalf("env fallback prefix = %q, want env-", got)
	}
}
