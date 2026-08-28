package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateAuthSecretsRequiresCursorHMACSecret(t *testing.T) {
	cfg := &Config{
		APIKey:           "api-key",
		AdminAPIKey:      "admin-key",
		SecretKey:        "jwt-secret",
		CursorHMACSecret: "",
	}
	if missing := cfg.ValidateAuthSecrets(); missing != "CURSOR_HMAC_SECRET (must be at least 32 bytes)" {
		t.Fatalf("ValidateAuthSecrets() = %q, want short-secret error", missing)
	}

	cfg.CursorHMACSecret = "cursor-secret-key-0123456789012345"
	if missing := cfg.ValidateAuthSecrets(); missing != "" {
		t.Fatalf("ValidateAuthSecrets() = %q, want empty", missing)
	}
}

func TestLoadReadsCursorHMACSecret(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "cursor-secret")
	if got := Load().CursorHMACSecret; got != "cursor-secret" {
		t.Fatalf("Load().CursorHMACSecret = %q, want cursor-secret", got)
	}
}

func TestLoad_TimeoutAndPendingTTLEnvironmentOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		want int
	}{
		{"pending ttl", "LLM_GATEWAY_PENDING_TTL_SECONDS", 600},
		{"upstream timeout", "LLM_GATEWAY_UPSTREAM_TIMEOUT", 121},
		{"stream timeout", "LLM_GATEWAY_STREAM_TIMEOUT", 901},
		{"chunk timeout", "LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", 301},
		{"first byte timeout", "LLM_GATEWAY_FIRST_BYTE_TIMEOUT", 31},
		{"keepalive interval", "LLM_GATEWAY_KEEPALIVE_INTERVAL", 16},
		{"SSE max line bytes", "LLM_GATEWAY_SSE_MAX_LINE_BYTES", 12345},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, ""+strconv.Itoa(tc.want))
			cfg := Load()
			var got int
			switch tc.key {
			case "LLM_GATEWAY_PENDING_TTL_SECONDS":
				got = cfg.PendingTTLSeconds
			case "LLM_GATEWAY_UPSTREAM_TIMEOUT":
				got = cfg.UpstreamTimeout
			case "LLM_GATEWAY_STREAM_TIMEOUT":
				got = cfg.StreamTimeout
			case "LLM_GATEWAY_STREAM_CHUNK_TIMEOUT":
				got = cfg.StreamChunkTimeout
			case "LLM_GATEWAY_FIRST_BYTE_TIMEOUT":
				got = cfg.FirstByteTimeout
			case "LLM_GATEWAY_KEEPALIVE_INTERVAL":
				got = cfg.KeepaliveInterval
			case "LLM_GATEWAY_SSE_MAX_LINE_BYTES":
				got = cfg.SSEMaxLineBytes
			}
			if got != tc.want {
				t.Fatalf("%s = %d, want %d", tc.key, got, tc.want)
			}
		})
	}
}

func TestLoad_DefaultStreamingTimeouts(t *testing.T) {
	for _, key := range []string{
		"LLM_GATEWAY_UPSTREAM_TIMEOUT",
		"LLM_GATEWAY_STREAM_CHUNK_TIMEOUT",
		"LLM_GATEWAY_FIRST_BYTE_TIMEOUT",
	} {
		t.Setenv(key, "")
	}

	cfg := Load()
	if cfg.UpstreamTimeout != 150 {
		t.Fatalf("UpstreamTimeout = %d, want 150", cfg.UpstreamTimeout)
	}
	// 2026-08-04: default raised 120 → 180 for reasoning models (see config.go).
	if cfg.FirstByteTimeout != 180 {
		t.Fatalf("FirstByteTimeout = %d, want 180", cfg.FirstByteTimeout)
	}
	if cfg.StreamChunkTimeout != 600 {
		t.Fatalf("StreamChunkTimeout = %d, want 600", cfg.StreamChunkTimeout)
	}
	// 2026-08-04: pre-stream keepalive is ON by default for all streaming
	// protocols — the primary fix for "agent task interrupted through the
	// gateway". Override via LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=false.
	if !cfg.EnablePreStreamKeepalive {
		t.Fatal("EnablePreStreamKeepalive = false, want true (2026-08-04 default)")
	}
}

func TestLoad_InvalidPendingTTLFallsBackToDefault(t *testing.T) {
	for _, value := range []string{"0", "-1", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_PENDING_TTL_SECONDS", value)
			if got := Load().PendingTTLSeconds; got != 300 {
				t.Fatalf("PendingTTLSeconds = %d, want 300", got)
			}
		})
	}
}
func TestLoad_EmptyStreamEarlyEmptyChunksEnvironmentOverride(t *testing.T) {
	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "5")
	if got := Load().EmptyStreamEarlyEmptyChunks; got != 5 {
		t.Fatalf("EmptyStreamEarlyEmptyChunks = %d, want 5", got)
	}

	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "0")
	if got := Load().EmptyStreamEarlyEmptyChunks; got != 0 {
		t.Fatalf("EmptyStreamEarlyEmptyChunks = %d, want explicit disable 0", got)
	}

	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "invalid")
	if got := Load().EmptyStreamEarlyEmptyChunks; got != 3 {
		t.Fatalf("invalid EmptyStreamEarlyEmptyChunks = %d, want default 3", got)
	}
}
func TestLoad_StreamRetryAndGateEnvironmentOverrides(t *testing.T) {
	for _, key := range []string{
		"LLM_GATEWAY_STREAM_RETRY_THRESHOLD",
		"LLM_GATEWAY_STREAM_RETRY_MAX_RETRIES",
		"LLM_GATEWAY_STREAM_RETRY_BASE_DELAY_MS",
		"LLM_GATEWAY_STREAM_RETRY_MAX_DELAY_MS",
		"LLM_GATEWAY_STREAM_RETRY_KEEPALIVE_SECS",
		"LLM_GATEWAY_ENABLE_EMPTY_STREAM_GATE",
		"LLM_GATEWAY_CREDENTIAL_FP_SLOT_ACTIVE_GATE_SECONDS",
		"LLM_GATEWAY_CREDENTIAL_FP_SLOT_RECLAIM_IDLE_SECONDS",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_THRESHOLD", "0")
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_MAX_RETRIES", "7")
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_BASE_DELAY_MS", "321")
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_MAX_DELAY_MS", "6543")
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_KEEPALIVE_SECS", "0")
	t.Setenv("LLM_GATEWAY_ENABLE_EMPTY_STREAM_GATE", "false")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_FP_SLOT_ACTIVE_GATE_SECONDS", "0")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_FP_SLOT_RECLAIM_IDLE_SECONDS", "0")

	cfg := Load()
	if cfg.StreamRetryThreshold != 0 || cfg.StreamRetryMaxRetries != 7 || cfg.StreamRetryBaseDelayMs != 321 || cfg.StreamRetryMaxDelayMs != 6543 || cfg.StreamRetryKeepaliveSecs != 10 {
		t.Fatalf("stream retry config = %#v, want threshold=0 max=7 base=321 cap=6543 keepalive default=10", cfg)
	}
	if cfg.EnableEmptyStreamGate {
		t.Fatal("EnableEmptyStreamGate = true, want explicit env false")
	}
	if cfg.CredentialFpSlotActiveGateSeconds != 0 || cfg.CredentialFpSlotReclaimIdleSeconds != 0 {
		t.Fatalf("fp slot gates = %d/%d, want explicit zero values", cfg.CredentialFpSlotActiveGateSeconds, cfg.CredentialFpSlotReclaimIdleSeconds)
	}
}

func TestLoadFileMergesStreamRetryAndGateValuesWithEnvPrecedence(t *testing.T) {
	keys := []string{
		"LLM_GATEWAY_STREAM_RETRY_THRESHOLD", "LLM_GATEWAY_STREAM_RETRY_MAX_RETRIES",
		"LLM_GATEWAY_STREAM_RETRY_BASE_DELAY_MS", "LLM_GATEWAY_STREAM_RETRY_MAX_DELAY_MS",
		"LLM_GATEWAY_STREAM_RETRY_KEEPALIVE_SECS", "LLM_GATEWAY_STREAM_RETRY_ENABLED",
		"LLM_GATEWAY_ENABLE_EMPTY_STREAM_GATE", "LLM_GATEWAY_ENABLE_CREDENTIAL_FP_SLOTS",
		"LLM_GATEWAY_CREDENTIAL_FP_SLOT_ACTIVE_GATE_SECONDS", "LLM_GATEWAY_CREDENTIAL_FP_SLOT_RECLAIM_IDLE_SECONDS",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := `stream_retry_threshold: 12
stream_retry_enabled: true
stream_retry_max_retries: 8
stream_retry_base_delay_ms: 333
stream_retry_max_delay_ms: 7777
stream_retry_keepalive_secs: 11
enable_empty_stream_gate: false
enable_credential_fp_slots: false
credential_fp_slot_active_gate_seconds: 17
credential_fp_slot_reclaim_idle_seconds: 19
`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Load()
	if err := cfg.LoadFile(path); err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if cfg.StreamRetryThreshold != 12 || !cfg.StreamRetryEnabled || cfg.StreamRetryMaxRetries != 8 || cfg.StreamRetryBaseDelayMs != 333 || cfg.StreamRetryMaxDelayMs != 7777 || cfg.StreamRetryKeepaliveSecs != 11 {
		t.Fatalf("YAML stream retry config not merged: %#v", cfg)
	}
	if cfg.EnableEmptyStreamGate || cfg.EnableCredentialFpSlots || cfg.CredentialFpSlotActiveGateSeconds != 17 || cfg.CredentialFpSlotReclaimIdleSeconds != 19 {
		t.Fatalf("YAML gate config not merged: %#v", cfg)
	}

	t.Setenv("LLM_GATEWAY_STREAM_RETRY_MAX_RETRIES", "4")
	t.Setenv("LLM_GATEWAY_ENABLE_EMPTY_STREAM_GATE", "true")
	t.Setenv("LLM_GATEWAY_ENABLE_CREDENTIAL_FP_SLOTS", "true")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_FP_SLOT_ACTIVE_GATE_SECONDS", "23")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_FP_SLOT_RECLAIM_IDLE_SECONDS", "29")
	cfg = Load()
	if err := cfg.LoadFile(path); err != nil {
		t.Fatalf("LoadFile() with env error = %v", err)
	}
	if cfg.StreamRetryMaxRetries != 4 || !cfg.EnableEmptyStreamGate || !cfg.EnableCredentialFpSlots || cfg.CredentialFpSlotActiveGateSeconds != 23 || cfg.CredentialFpSlotReclaimIdleSeconds != 29 {
		t.Fatalf("environment did not take precedence: %#v", cfg)
	}
}

func TestParseCommaList(t *testing.T) {
	got := parseCommaList("workspaceId, room_session_key , ,chatRoomId")
	want := []string{"workspaceId", "room_session_key", "chatRoomId"}
	if len(got) != len(want) {
		t.Fatalf("len(parseCommaList()) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseCommaList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConfigYAMLSessionIDBodyKeysArray(t *testing.T) {
	var cfg Config
	err := yaml.Unmarshal([]byte("session_id_body_keys:\n  - workspaceId\n  - room_session_key\n"), &cfg)
	if err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.SessionIDBodyKeys) != 2 {
		t.Fatalf("len(SessionIDBodyKeys) = %d, want 2", len(cfg.SessionIDBodyKeys))
	}
	if cfg.SessionIDBodyKeys[0] != "workspaceId" || cfg.SessionIDBodyKeys[1] != "room_session_key" {
		t.Fatalf("SessionIDBodyKeys = %#v", cfg.SessionIDBodyKeys)
	}
}

func TestLoadModelAliasPrefixDefaultsAndEnv(t *testing.T) {
	previous, wasSet := os.LookupEnv("LLM_GATEWAY_MODEL_ALIAS_PREFIX")
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", previous)
		} else {
			_ = os.Unsetenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX")
		}
	})
	_ = os.Unsetenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX")
	if got := Load().ModelAliasPrefix; got != "kx-" {
		t.Fatalf("unset env ModelAliasPrefix = %q, want kx-", got)
	}

	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "")
	if got := Load().ModelAliasPrefix; got != "" {
		t.Fatalf("empty env ModelAliasPrefix = %q, want empty", got)
	}

	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "alias-")
	if got := Load().ModelAliasPrefix; got != "alias-" {
		t.Fatalf("custom env ModelAliasPrefix = %q, want alias-", got)
	}
}

func TestLoadFileModelAliasPrefixCanBeEmpty(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "")
	cfg := Load()
	_ = os.Unsetenv("LLM_GATEWAY_MODEL_ALIAS_PREFIX")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("model_alias_prefix: \"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.LoadFile(path); err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if cfg.ModelAliasPrefix != "" {
		t.Fatalf("empty YAML ModelAliasPrefix = %q, want empty", cfg.ModelAliasPrefix)
	}
}
