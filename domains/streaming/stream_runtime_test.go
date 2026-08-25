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
	streamConfigStore.Store(nil)
	if got := ModelAliasPrefix(); got != "" {
		t.Fatalf("explicit empty env alias prefix = %q, want empty", got)
	}
	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "alias-")
	if got := ModelAliasPrefix(); got != "alias-" {
		t.Fatalf("env alias prefix = %q, want alias-", got)
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
	if got := ModelAliasPrefix(); got != "" {
		t.Fatalf("empty env fallback prefix = %q, want empty", got)
	}

	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "env-")
	if got := ModelAliasPrefix(); got != "env-" {
		t.Fatalf("env fallback prefix = %q, want env-", got)
	}
}

func TestApplyAliasPrefix(t *testing.T) {
	previous := streamConfigStore.Load()
	t.Cleanup(func() { streamConfigStore.Store(previous) })
	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "")

	t.Run("default kx prefix strips alias", func(t *testing.T) {
		streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "kx-"}))
		if got := ApplyAliasPrefix("kx-gpt-5.6-terra"); got != "gpt-5.6-terra" {
			t.Errorf("ApplyAliasPrefix(%q) = %q, want %q",
				"kx-gpt-5.6-terra", got, "gpt-5.6-terra")
		}
	})

	t.Run("non-prefixed model unchanged", func(t *testing.T) {
		streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "kx-"}))
		if got := ApplyAliasPrefix("gpt-5.6-terra"); got != "gpt-5.6-terra" {
			t.Errorf("ApplyAliasPrefix(%q) = %q, want %q",
				"gpt-5.6-terra", got, "gpt-5.6-terra")
		}
	})

	t.Run("disabled prefix returns input unchanged", func(t *testing.T) {
		streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: ""}))
		if got := ApplyAliasPrefix("kx-gpt-5.6-terra"); got != "kx-gpt-5.6-terra" {
			t.Errorf("ApplyAliasPrefix(%q) with empty prefix = %q, want %q",
				"kx-gpt-5.6-terra", got, "kx-gpt-5.6-terra")
		}
	})

	t.Run("custom prefix works", func(t *testing.T) {
		streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "myalias-"}))
		if got := ApplyAliasPrefix("myalias-claude-opus"); got != "claude-opus" {
			t.Errorf("ApplyAliasPrefix(%q) = %q, want %q",
				"myalias-claude-opus", got, "claude-opus")
		}
	})

	t.Run("case-insensitive stripping", func(t *testing.T) {
		streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "kx-"}))
		if got := ApplyAliasPrefix("KX-GPT-5.6-terra"); got != "GPT-5.6-terra" {
			t.Errorf("ApplyAliasPrefix(%q) = %q, want %q",
				"KX-GPT-5.6-terra", got, "GPT-5.6-terra")
		}
	})

	t.Run("hot-reload reflects new prefix", func(t *testing.T) {
		streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "old-"}))
		if got := ApplyAliasPrefix("old-gpt-4"); got != "gpt-4" {
			t.Fatalf("setup: ApplyAliasPrefix(old) = %q, want gpt-4", got)
		}
		streamConfigStore.Store(config.NewStore(&config.Config{ModelAliasPrefix: "new-"}))
		if got := ApplyAliasPrefix("new-gpt-4"); got != "gpt-4" {
			t.Errorf("after reload ApplyAliasPrefix(new) = %q, want gpt-4", got)
		}
		if got := ApplyAliasPrefix("old-gpt-4"); got != "old-gpt-4" {
			t.Errorf("after reload old prefix should NOT match new: got %q", got)
		}
	})
}
