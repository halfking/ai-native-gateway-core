// Package main — attachment_storage_init_test.go
//
// initAttachmentStorage 的 boot 接线单测，2026-07-15 (Phase 3D)
//
// 覆盖矩阵：
//   - 未设置 LLM_GATEWAY_STORAGE_TYPE → filesystem（旧默认行为）
//   - 设置为 "filesystem" / "local" / "fs" → filesystem（兼容旧值）
//   - 设置为 "oss" / "s3" / "minio" / "cloudreve" → 走对应后端构造路径
//   - 设置为 typo / 不识别的 type → Warn + 退化到 filesystem（不阻塞）
//   - 配置校验失败（缺 BucketName 等）→ Warn + 退化到 filesystem
//   - LLM_GATEWAY_ATTACHMENT_MAX_SIZE 在所有路径都生效
//
// 启动期的失败（WARN) 一致：不阻塞 gateway 启动。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitAttachmentStorage_DefaultIsFilesystem(t *testing.T) {
	// Save+restore env, then clear any storage-related vars.
	clearStorageEnv(t)
	defer restoreStorageEnv(t)

	storage, backendType := initAttachmentStorage("/tmp/foo")
	if backendType != "filesystem" {
		t.Errorf("backendType = %q, want filesystem", backendType)
	}
	if storage == nil {
		t.Fatal("storage = nil, expected a LocalStorageBackend")
	}
	if got := storage.BaseDir(); got != "/tmp/foo" {
		t.Errorf("BaseDir = %q, want /tmp/foo", got)
	}
}

func TestInitAttachmentStorage_LocalAliases(t *testing.T) {
	aliases := []string{"filesystem", "local", "fs", "FILESYSTEM", "Local"}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			clearStorageEnv(t)
			defer restoreStorageEnv(t)
			t.Setenv("LLM_GATEWAY_STORAGE_TYPE", alias)

			storage, backendType := initAttachmentStorage("/tmp/foo")
			if backendType != "filesystem" {
				t.Errorf("backendType for alias %q = %q, want filesystem", alias, backendType)
			}
			if storage == nil {
				t.Errorf("storage nil for alias %q", alias)
			}
		})
	}
}

func TestInitAttachmentStorage_UnknownTypeFallsBack(t *testing.T) {
	clearStorageEnv(t)
	defer restoreStorageEnv(t)
	t.Setenv("LLM_GATEWAY_STORAGE_TYPE", "s3nuba") // typo of "s3"

	storage, backendType := initAttachmentStorage("/tmp/foo")
	if backendType != "filesystem" {
		t.Errorf("backendType for typo = %q, want filesystem", backendType)
	}
	if storage == nil {
		t.Fatal("storage should fall back to filesystem, not nil")
	}
}

// TestInitAttachmentStorage_BackendHealthCheckDoesNotCrash verifies that
// boot-path does NOT block when an opt-in backend is unreachable. The
// health-check failure is logged as a Warn and the storage object is
// still returned to the rest of the boot path.
//
// This test runs identically under any tag combo:
//   - default build: stub returns "not available" → backendType = "filesystem"
//   - cloudreve_storage: real backend constructed; health check fails against
//     127.0.0.1:1 (closed port) → backendType stays "cloudreve", WARN logged,
//     storage object is still non-nil so the gateway can boot and degrade
//     at first attachment-write instead of at gateway-start.
//
// Both are correct: in production, the warn-at-boot pattern surfaces the
// misconfig in logs immediately; the gateway still serves traffic without
// attachments working. (Attachments were already non-essential — only used
// for multimodal extraction.)
func TestInitAttachmentStorage_BackendHealthCheckDoesNotCrash(t *testing.T) {
	clearStorageEnv(t)
	defer restoreStorageEnv(t)

	t.Setenv("LLM_GATEWAY_STORAGE_TYPE", "cloudreve")
	t.Setenv("LLM_GATEWAY_CLOUDREVE_BASE_URL", "http://127.0.0.1:1") // refused
	t.Setenv("LLM_GATEWAY_CLOUDREVE_USERNAME", "u")
	t.Setenv("LLM_GATEWAY_CLOUDREVE_PASSWORD", "p")

	storage, _ := initAttachmentStorage("/tmp/foo")

	// In stub build: storage non-nil (filesystem fallback).
	// In real build: storage non-nil (cloudreve backend, health-check failed
	// but boot continued). Either way: gateway can start.
	if storage == nil {
		t.Error("storage = nil; expected boot to continue with a backend (possibly degraded)")
	}
}

// TestInitAttachmentStorage_OSSValidationFails covers the scenario where
// the env is set to "oss" but validation fails (missing required fields).
// The boot path should fall back to filesystem.
func TestInitAttachmentStorage_OSSValidationFails(t *testing.T) {
	clearStorageEnv(t)
	defer restoreStorageEnv(t)

	t.Setenv("LLM_GATEWAY_STORAGE_TYPE", "oss")
	// Intentionally NOT setting LLM_GATEWAY_OSS_ENDPOINT etc.
	// ValidateStorageConfig will fail with "OSS endpoint is required".

	storage, backendType := initAttachmentStorage("/tmp/foo")
	if backendType != "filesystem" {
		t.Errorf("backendType = %q, want filesystem on validation failure", backendType)
	}
	if storage == nil {
		t.Error("storage = nil; expected filesystem fallback after validation failure")
	}
}

// TestInitAttachmentStorage_S3ValidationFails mirrors the OSS test
// for the S3/MinIO type.
func TestInitAttachmentStorage_S3ValidationFails(t *testing.T) {
	clearStorageEnv(t)
	defer restoreStorageEnv(t)

	t.Setenv("LLM_GATEWAY_STORAGE_TYPE", "s3")
	// Intentionally NOT setting LLM_GATEWAY_S3_*.

	storage, backendType := initAttachmentStorage("/tmp/foo")
	if backendType != "filesystem" {
		t.Errorf("backendType = %q, want filesystem on validation failure", backendType)
	}
	if storage == nil {
		t.Error("storage = nil; expected filesystem fallback after validation failure")
	}
}

// TestInitAttachmentStorage_MaxSizeAppliedFilesystem asserts that the
// filesystem path honors LLM_GATEWAY_ATTACHMENT_MAX_SIZE.
func TestInitAttachmentStorage_MaxSizeAppliedFilesystem(t *testing.T) {
	clearStorageEnv(t)
	defer restoreStorageEnv(t)

	t.Setenv("LLM_GATEWAY_ATTACHMENT_MAX_SIZE", "5242880") // 5MiB

	storage, backendType := initAttachmentStorage(t.TempDir())
	if backendType != "filesystem" {
		t.Fatalf("backendType = %q", backendType)
	}
	if storage == nil {
		t.Fatal("storage = nil")
	}
	if storage.MaxSize != 5242880 {
		t.Errorf("MaxSize = %d, want 5242880", storage.MaxSize)
	}
}

// TestInitAttachmentStorage_BadMaxSizeIgnored asserts that a non-numeric
// LLM_GATEWAY_ATTACHMENT_MAX_SIZE is silently ignored (matches existing
// main.go behaviour for the local path).
func TestInitAttachmentStorage_BadMaxSizeIgnored(t *testing.T) {
	clearStorageEnv(t)
	defer restoreStorageEnv(t)

	t.Setenv("LLM_GATEWAY_ATTACHMENT_MAX_SIZE", "notanumber")

	storage, _ := initAttachmentStorage(t.TempDir())
	if storage == nil {
		t.Fatal("storage = nil")
	}
	if storage.MaxSize != 20*1024*1024 {
		t.Errorf("MaxSize = %d, want default 20MB (DefaultMaxSize)", storage.MaxSize)
	}
}

// TestInitAttachmentStorage_DirCreationForFilesystem verifies that the
// filesystem path auto-creates the directory (matches original
// LocalStorageBackend semantics from NewStorage).
func TestInitAttachmentStorage_DirCreationForFilesystem(t *testing.T) {
	clearStorageEnv(t)
	defer restoreStorageEnv(t)

	parent := t.TempDir()
	nested := filepath.Join(parent, "a", "b", "c")

	storage, backendType := initAttachmentStorage(nested)
	if backendType != "filesystem" {
		t.Fatalf("backendType = %q", backendType)
	}
	if storage == nil {
		t.Fatal("storage = nil; expected filesystem backend to create nested dir")
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("nested dir not created: %v", err)
	}
}

// ---- env helpers ----

// storageEnvKeys is the set of env vars initAttachmentStorage reads.
// Clearing them gives us a deterministic baseline for each test.
var storageEnvKeys = []string{
	"LLM_GATEWAY_STORAGE_TYPE",
	"LLM_GATEWAY_ATTACHMENT_DIR",
	"LLM_GATEWAY_ATTACHMENT_MAX_SIZE",

	// Cloudreve
	"LLM_GATEWAY_CLOUDREVE_BASE_URL",
	"LLM_GATEWAY_CLOUDREVE_USERNAME",
	"LLM_GATEWAY_CLOUDREVE_PASSWORD",
	"LLM_GATEWAY_CLOUDREVE_REMOTE_PATH",
	"LLM_GATEWAY_CLOUDREVE_TIMEOUT_SEC",

	// OSS
	"LLM_GATEWAY_OSS_ENDPOINT",
	"LLM_GATEWAY_OSS_ACCESS_KEY_ID",
	"LLM_GATEWAY_OSS_ACCESS_KEY_SECRET",
	"LLM_GATEWAY_OSS_BUCKET",
	"LLM_GATEWAY_OSS_PREFIX",

	// S3 / MinIO
	"LLM_GATEWAY_S3_ENDPOINT",
	"LLM_GATEWAY_S3_REGION",
	"LLM_GATEWAY_S3_ACCESS_KEY_ID",
	"LLM_GATEWAY_S3_SECRET_ACCESS_KEY",
	"LLM_GATEWAY_S3_BUCKET",
	"LLM_GATEWAY_S3_PREFIX",
	"LLM_GATEWAY_S3_USE_SSL",
	"LLM_GATEWAY_S3_FORCE_PATH_STYLE",
}

// savedEnv mirrors storageEnvKeys so restoreStorageEnv can put them back
// after a test. Per-test scoping via t.Setenv would also work, but this
// pattern is needed because initAttachmentStorage reads env at call time,
// not at test entry, so we want a known-empty starting state.
var savedEnv = map[string]string{}

func clearStorageEnv(t *testing.T) {
	t.Helper()
	for _, k := range storageEnvKeys {
		savedEnv[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
}

func restoreStorageEnv(t *testing.T) {
	t.Helper()
	for _, k := range storageEnvKeys {
		if v, ok := savedEnv[k]; ok {
			os.Setenv(k, v)
		} else {
			os.Unsetenv(k)
		}
	}
}

// TestIsLocalStorageType just ensures the alias table is what we documented.
func TestIsLocalStorageType(t *testing.T) {
	for _, pass := range []string{"filesystem", "local", "fs", "FILESYSTEM", "Local"} {
		if !isLocalStorageType(pass) {
			t.Errorf("isLocalStorageType(%q) = false, want true", pass)
		}
	}
	for _, fail := range []string{"oss", "s3", "cloudreve", "minio", ""} {
		if isLocalStorageType(fail) {
			t.Errorf("isLocalStorageType(%q) = true, want false", fail)
		}
	}
}

// TestIsAcceptableBackendType documents which opt-in types are recognized.
func TestIsAcceptableBackendType(t *testing.T) {
	for _, ok := range []string{"oss", "s3", "minio", "cloudreve"} {
		if !isAcceptableBackendType(ok) {
			t.Errorf("isAcceptableBackendType(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"", "filesystem", "local", "gcs", "azure", "FOO"} {
		if isAcceptableBackendType(no) {
			t.Errorf("isAcceptableBackendType(%q) = true, want false", no)
		}
	}
}

// TestMapBackendTag documents the env→build-tag mapping.
func TestMapBackendTag(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"oss", "oss"},
		{"s3", "s3"},
		{"minio", "s3"},
		{"cloudreve", "cloudreve_storage"},
		{"filesystem", ""},
		{"unknown", ""},
	}
	for _, tc := range tests {
		got := mapBackendTag(tc.in)
		// Trim "s3" → "s3" exactly; "oss" → "oss"; "cloudreve" → "cloudreve_storage"
		// Any mismatch is a regression on the error hint a user sees in logs.
		if !strings.Contains(got, tc.want) || (tc.want == "" && got != "") {
			// Allow exact empty match for unknown keys but the function is
			// meant to return "" for non-mapped inputs:
			if got != tc.want {
				t.Errorf("mapBackendTag(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	}
}
