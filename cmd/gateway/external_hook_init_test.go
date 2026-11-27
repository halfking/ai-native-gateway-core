package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testContext() context.Context { return context.Background() }

// TestLoadExternalHookConfigDefaultOff 钉住 W3 外部钩子默认关——
// 未显式开启时绝不装配（安全默认值，2026-11-27 生产装配面）。
func TestLoadExternalHookConfigDefaultOff(t *testing.T) {
	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOK_ENABLED", "")
	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOK_MANIFEST", "")
	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOKS_DIR", "")

	cfg := loadExternalHookConfig()
	if cfg.Enabled {
		t.Fatal("external hook must default to OFF")
	}
	if len(cfg.ManifestPaths) != 0 {
		t.Fatalf("no manifest paths expected by default, got %v", cfg.ManifestPaths)
	}
	if cfg.Timeout != time.Second {
		t.Errorf("default timeout = %v, want 1s", cfg.Timeout)
	}
}

// TestLoadExternalHookConfigExplicit 钉住显式开启时的配置读取。
func TestLoadExternalHookConfigExplicit(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "hook.json")
	if err := os.WriteFile(manifest, []byte(`{"plugin_id":"demo-hook"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOK_ENABLED", "1")
	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOK_MANIFEST", manifest)
	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOK_TIMEOUT_MS", "2500")
	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOK_CIRCUIT_THRESHOLD", "3")
	t.Setenv("LLM_GATEWAY_EXTERNAL_HOOK_ALLOW_REMOTE_BODY", "true")

	cfg := loadExternalHookConfig()
	if !cfg.Enabled {
		t.Fatal("external hook should be enabled")
	}
	if len(cfg.ManifestPaths) != 1 || cfg.ManifestPaths[0] != manifest {
		t.Fatalf("manifest paths = %v, want [%s]", cfg.ManifestPaths, manifest)
	}
	if cfg.Timeout != 2500*time.Millisecond {
		t.Errorf("timeout = %v, want 2.5s", cfg.Timeout)
	}
	if cfg.CircuitThreshold != 3 {
		t.Errorf("circuit threshold = %d, want 3", cfg.CircuitThreshold)
	}
	if !cfg.AllowRemoteBodyCapture {
		t.Error("allow remote body should be true")
	}
}

// TestLoadHookManifestMissingPluginID 钉住 manifest 校验：缺 plugin_id 拒绝装载。
func TestLoadHookManifestMissingPluginID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"display_name":"no id"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHookManifest(path); err == nil {
		t.Fatal("manifest without plugin_id should be rejected")
	}
}

// TestInitExternalHooksDisabledReturnsNil 钉住开关关闭时 initExternalHooks 不装载。
func TestInitExternalHooksDisabledReturnsNil(t *testing.T) {
	cfg := externalHookConfig{Enabled: false, ManifestPaths: []string{"/tmp/x.json"}}
	if got := initExternalHooks(testContext(), cfg, nil); got != nil {
		t.Fatal("disabled external hook must return nil integrator")
	}
}

// TestInitExternalHooksNoManifestWarnsAndSkips 钉住开启但无 manifest 时不装载。
func TestInitExternalHooksNoManifestWarnsAndSkips(t *testing.T) {
	cfg := externalHookConfig{Enabled: true}
	if got := initExternalHooks(testContext(), cfg, nil); got != nil {
		t.Fatal("enabled without manifests must return nil integrator")
	}
}
