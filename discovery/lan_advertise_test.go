package discovery

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

func TestLANAdvertiser_StartStop(t *testing.T) {
	adv := NewLANAdvertiser("test-gateway", 8781, "2.5.0", []string{"openai", "anthropic"})

	ctx := context.Background()
	if err := adv.Start(ctx); err != nil {
		t.Fatalf("Failed to start advertiser: %v", err)
	}

	if !adv.IsRunning() {
		t.Error("Advertiser should be running")
	}

	status := adv.Status()
	if status["running"] != true {
		t.Error("Status should show running=true")
	}

	adv.Stop()

	if adv.IsRunning() {
		t.Error("Advertiser should not be running after Stop()")
	}
}

func TestLANAdvertiser_DoubleStart(t *testing.T) {
	adv := NewLANAdvertiser("test-gateway", 8782, "2.5.0", []string{"openai"})
	defer adv.Stop()

	ctx := context.Background()
	if err := adv.Start(ctx); err != nil {
		t.Fatalf("First start failed: %v", err)
	}

	// Second start should fail
	if err := adv.Start(ctx); err == nil {
		t.Error("Second start should return an error")
	}
}

func TestLANAdvertiser_Restart(t *testing.T) {
	adv := NewLANAdvertiser("test-gateway", 8783, "2.5.0", []string{"openai"})

	ctx := context.Background()

	// First cycle
	if err := adv.Start(ctx); err != nil {
		t.Fatalf("First start failed: %v", err)
	}
	if !adv.IsRunning() {
		t.Error("Should be running after first start")
	}
	adv.Stop()
	if adv.IsRunning() {
		t.Error("Should not be running after first stop")
	}

	// Second cycle (restart)
	if err := adv.Start(ctx); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	if !adv.IsRunning() {
		t.Error("Should be running after restart")
	}
	adv.Stop()
	if adv.IsRunning() {
		t.Error("Should not be running after second stop")
	}
}

func TestLANAdvertiser_ContextCancellation(t *testing.T) {
	adv := NewLANAdvertiser("test-gateway", 8784, "2.5.0", []string{"openai"})

	ctx, cancel := context.WithCancel(context.Background())

	if err := adv.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !adv.IsRunning() {
		t.Error("Should be running")
	}

	// Cancel context should trigger Stop()
	cancel()
	time.Sleep(100 * time.Millisecond) // Give it time to process cancellation

	if adv.IsRunning() {
		t.Error("Should have stopped after context cancellation")
	}
}

func TestParseServiceEntry(t *testing.T) {
	// Test TXT record parsing without network multicast
	entry := &mdns.ServiceEntry{
		Name:   "test-gateway",
		Host:   "gateway.local.",
		AddrV4: net.ParseIP("192.168.1.100"),
		Port:   8781,
		InfoFields: []string{
			"version=2.5.0",
			"api=openai,anthropic,gemini",
			"proto=http",
		},
	}

	gw := parseServiceEntry(entry)
	if gw == nil {
		t.Fatal("parseServiceEntry returned nil")
	}

	// R39 pin: live entries carry the full service instance name; the bare
	// instance name is what callers match on.
	if full := parseServiceEntry(&mdns.ServiceEntry{
		Name:   "test-gateway._llm-gateway._tcp.local.",
		AddrV4: net.ParseIP("192.168.1.100"),
		Port:   8781,
	}); full.Name != "test-gateway" {
		t.Errorf("Expected full SPN trimmed to 'test-gateway', got %q", full.Name)
	}

	if gw.Name != "test-gateway" {
		t.Errorf("Expected name 'test-gateway', got %s", gw.Name)
	}
	if gw.Port != 8781 {
		t.Errorf("Expected port 8781, got %d", gw.Port)
	}
	if gw.Version != "2.5.0" {
		t.Errorf("Expected version '2.5.0', got %s", gw.Version)
	}
	if len(gw.APIs) != 3 {
		t.Errorf("Expected 3 APIs, got %d", len(gw.APIs))
	}
	if gw.Address != "http://192.168.1.100:8781" {
		t.Errorf("Expected address 'http://192.168.1.100:8781', got %s", gw.Address)
	}
	if gw.TXTRecord["proto"] != "http" {
		t.Errorf("Expected proto=http in TXT, got %s", gw.TXTRecord["proto"])
	}
}

// TestDiscoverGateways is skipped by default because it requires real network multicast
// which may not work reliably in all environments (firewalls, network policies, etc.)
func TestDiscoverGateways(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping network-dependent discovery test in short mode")
	}

	// Start an advertiser
	adv := NewLANAdvertiser("test-discover", 8785, "2.5.0", []string{"openai", "anthropic"})
	defer adv.Stop()

	ctx := context.Background()
	if err := adv.Start(ctx); err != nil {
		t.Fatalf("Failed to start advertiser: %v", err)
	}

	// Give mDNS time to propagate
	time.Sleep(500 * time.Millisecond)

	// Discover gateways
	gateways, err := DiscoverGateways(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("Discovery failed: %v", err)
	}

	// Log all found gateways (may find others on the network)
	t.Logf("Found %d gateway(s)", len(gateways))
	found := false
	for _, gw := range gateways {
		t.Logf("Found gateway: %s at %s (version=%s, apis=%v)", gw.Name, gw.Address, gw.Version, gw.APIs)
		if gw.Name == "test-discover" && gw.Port == 8785 {
			found = true
			if gw.Version != "2.5.0" {
				t.Errorf("Expected version 2.5.0, got %s", gw.Version)
			}
			if len(gw.APIs) != 2 {
				t.Errorf("Expected 2 APIs, got %d", len(gw.APIs))
			}
		}
	}

	if !found {
		t.Error("Did not discover our test gateway")
	}
}
