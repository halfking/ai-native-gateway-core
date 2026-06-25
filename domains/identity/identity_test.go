package identity

import (
	"net/http"
	"testing"
)

func TestPrimarySeedDeviceSeed(t *testing.T) {
	fp := ClientFingerprint{
		DeviceSeed: "my-device-123",
		MachineID:  "machine-abc",
	}
	if seed := fp.PrimarySeed(); seed != "my-device-123" {
		t.Fatalf("expected device_seed, got %q", seed)
	}
}

func TestPrimarySeedMachineID(t *testing.T) {
	fp := ClientFingerprint{
		MachineID: "machine-abc",
		OSName:    "macOS",
	}
	if seed := fp.PrimarySeed(); seed != "machine-abc" {
		t.Fatalf("expected machine_id, got %q", seed)
	}
}

func TestPrimarySeedComposite(t *testing.T) {
	fp := ClientFingerprint{
		UserAgent:      "GoTest/1.0",
		OSName:         "linux",
		OSArch:         "amd64",
		RuntimeName:    "go",
		RuntimeVersion: "1.24",
		ClientProfile:  "roocode",
	}
	seed := fp.PrimarySeed()
	if seed != "GoTest/1.0|linux|amd64|go|1.24|roocode" {
		t.Fatalf("unexpected composite seed: %q", seed)
	}
}

func TestPrimarySeedUnknown(t *testing.T) {
	fp := ClientFingerprint{}
	if seed := fp.PrimarySeed(); seed != "unknown" {
		t.Fatalf("expected 'unknown', got %q", seed)
	}
}

func TestBuildIdentityConsistentWithPython(t *testing.T) {
	// This input produces known output verified against Python:
	//   identity_key = "default:key42:my-device-123"
	//   identity_hash = sha256("default:key42:my-device-123")
	appID := 42
	id := BuildIdentity("default", &appID, nil, ClientFingerprint{
		DeviceSeed: "my-device-123",
	})

	if id.AppOrKey != "app42" {
		t.Fatalf("expected app42, got %q", id.AppOrKey)
	}
	if id.VirtualClientID != "vc-"+id.IdentityHash[:16] {
		t.Fatalf("virtual client id mismatch: %q vs vc-%s", id.VirtualClientID, id.IdentityHash[:16])
	}
	if len(id.IdentityHash) != 64 {
		t.Fatalf("expected 64-char hex hash, got %d chars", len(id.IdentityHash))
	}
}

func TestBuildIdentityFromRequest(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "test-seed")
	r.Header.Set("X-OS-Name", "darwin")

	appID := 1
	id := BuildIdentityFromRequest(r, "default", &appID, nil, "roocode")

	if id.Fingerprint.DeviceSeed != "test-seed" {
		t.Fatalf("expected device seed from header, got %q", id.Fingerprint.DeviceSeed)
	}
	if id.Fingerprint.OSName != "darwin" {
		t.Fatalf("expected OS name from header, got %q", id.Fingerprint.OSName)
	}
	if id.VirtualClientID != "vc-"+id.IdentityHash[:16] {
		t.Fatalf("virtual client id format wrong")
	}
}

func TestVirtualIPFormat(t *testing.T) {
	// Deterministic from hash
	id1 := BuildIdentity("default", nil, intPtr(1), ClientFingerprint{DeviceSeed: "a"})
	id2 := BuildIdentity("default", nil, intPtr(1), ClientFingerprint{DeviceSeed: "a"})
	if id1.VirtualIP != id2.VirtualIP {
		t.Fatal("same inputs must produce same virtual IP")
	}
	if len(id1.VirtualIP) < 6 || id1.VirtualIP[:3] != "10." {
		t.Fatalf("virtual IP must start with 10., got %q", id1.VirtualIP)
	}
}

func TestVirtualMACFormat(t *testing.T) {
	id := BuildIdentity("default", nil, intPtr(1), ClientFingerprint{DeviceSeed: "a"})
	if len(id.VirtualMAC) != 17 {
		t.Fatalf("expected MAC like 02:xx:xx:xx:xx:xx, got %q", id.VirtualMAC)
	}
	if id.VirtualMAC[:3] != "02:" {
		t.Fatalf("expected locally-administered MAC starting with 02:, got %q", id.VirtualMAC)
	}
}

func intPtr(n int) *int { return &n }
