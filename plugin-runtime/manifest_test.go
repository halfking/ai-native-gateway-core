package pluginruntime

import "testing"

func TestLoadManifest_ValidatesContract(t *testing.T) {
	m, err := LoadManifest("testdata/ai-session-manager.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.PluginVersion != "0.1.1" {
		t.Fatalf("plugin_version = %q", m.PluginVersion)
	}
	if m.BuildSeq != 2 {
		t.Fatalf("build_seq = %d", m.BuildSeq)
	}
	if m.GatewayCompatibility.APIContract != "gateway-plugin-v1" {
		t.Fatalf("contract = %q", m.GatewayCompatibility.APIContract)
	}
	if m.Activation.ModuleKey != "session_manager" {
		t.Fatalf("activation.module_key = %q", m.Activation.ModuleKey)
	}
	if !m.Activation.LicenseRequired {
		t.Fatal("session-manager fixture must require an entitlement")
	}
	if len(m.Pages) != 2 {
		t.Fatalf("page count = %d", len(m.Pages))
	}
	if m.Pages[0].Nav.LabelKey != "nav.item.sessionPlugin" {
		t.Fatalf("sessions nav.label_key = %q", m.Pages[0].Nav.LabelKey)
	}
	if m.Pages[1].Nav.LabelKey != "nav.item.sessionPluginSettings" {
		t.Fatalf("settings nav.label_key = %q", m.Pages[1].Nav.LabelKey)
	}
}

func TestLoadManifest_RejectsBadContract(t *testing.T) {
	_, err := LoadManifest("testdata/bad_contract.json")
	if err == nil {
		t.Fatal("expected error")
	}
}
