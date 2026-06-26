package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

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
