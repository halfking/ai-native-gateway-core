package config

import (
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
