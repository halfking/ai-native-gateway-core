package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReloadFileNormalizesExclusiveRetryOwner(t *testing.T) {
	t.Setenv("LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED", "")
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_ENABLED", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yml")
	if err := os.WriteFile(path, []byte("request_survival_enabled: true\nstream_retry_enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(&Config{})
	if err := store.ReloadFile(path); err != nil {
		t.Fatalf("ReloadFile() error = %v", err)
	}
	cfg := store.Get()
	if !cfg.RequestSurvivalEnabled {
		t.Fatal("request survival should remain enabled")
	}
	if cfg.StreamRetryEnabled {
		t.Fatal("stream retry must be disabled when survival owns outer retries")
	}
	if cfg.RequestSurvivalInteractiveDeadlineSeconds != 18000 {
		t.Fatalf("interactive deadline = %d, want normalized default", cfg.RequestSurvivalInteractiveDeadlineSeconds)
	}
}
