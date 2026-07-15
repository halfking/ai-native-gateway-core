package centeragent

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveInstanceID_Explicit(t *testing.T) {
	id, err := resolveInstanceID("node-154", t.TempDir())
	if err != nil {
		t.Fatalf("resolveInstanceID: %v", err)
	}
	if id != "node-154" {
		t.Fatalf("got %q want node-154", id)
	}
}

func TestGetOrCreateInstanceID_Persists(t *testing.T) {
	dir := t.TempDir()
	id1, err := getOrCreateInstanceID(dir)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty id")
	}

	data, err := os.ReadFile(filepath.Join(dir, "instance.id"))
	if err != nil {
		t.Fatalf("read instance.id: %v", err)
	}
	if string(data) != id1 {
		t.Fatalf("file content mismatch")
	}

	id2, err := getOrCreateInstanceID(dir)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected stable id, got %q then %q", id1, id2)
	}
}

func TestFirstNonLoopbackIP_ReturnsStringOrEmpty(t *testing.T) {
	ip := firstNonLoopbackIP()
	if ip == "" {
		return
	}
	if net.ParseIP(ip) == nil {
		t.Fatalf("invalid ip %q", ip)
	}
}
