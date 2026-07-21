package store

import "testing"

func TestNodeKeyV2(t *testing.T) {
	if k := NodeKey("ursm:v2:", 12, "gpt-4"); k != "ursm:v2:node:12:gpt-4" {
		t.Fatalf("unexpected key %q", k)
	}
}

func TestWindowKeysDiffer(t *testing.T) {
	a := WindowKey("ursm:v2:", 12, "gpt-4", "1m")
	b := WindowKey("ursm:v2:", 12, "gpt-4", "5m")
	if a == b {
		t.Fatalf("windows must not collide")
	}
}

func TestReadyKey(t *testing.T) {
	if k := ReadyKey("ursm:v2:"); k != "ursm:v2:meta:ready" {
		t.Fatalf("unexpected ready key %q", k)
	}
}
