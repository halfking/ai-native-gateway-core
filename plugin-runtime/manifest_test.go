package pluginruntime

import "testing"

func TestLoadManifest_ValidatesContract(t *testing.T) {
	m, err := LoadManifest("testdata/ai-session-manager.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.GatewayCompatibility.APIContract != "gateway-plugin-v1" {
		t.Fatalf("contract = %q", m.GatewayCompatibility.APIContract)
	}
}

func TestLoadManifest_RejectsBadContract(t *testing.T) {
	_, err := LoadManifest("testdata/bad_contract.json")
	if err == nil {
		t.Fatal("expected error")
	}
}
