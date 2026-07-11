package main

import "testing"

func TestIsLoopbackListenAddr(t *testing.T) {
	for _, address := range []string{"127.0.0.1:6060", "localhost:6060", "[::1]:6060"} {
		if !isLoopbackListenAddr(address) {
			t.Errorf("expected loopback address %q", address)
		}
	}
	for _, address := range []string{"0.0.0.0:6060", ":6060", "10.0.0.1:6060", "bad"} {
		if isLoopbackListenAddr(address) {
			t.Errorf("expected non-loopback address %q", address)
		}
	}
}
