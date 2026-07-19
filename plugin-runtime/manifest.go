package pluginruntime

import (
	"encoding/json"
	"fmt"
	"os"
)

func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.PluginID == "" {
		return fmt.Errorf("plugin_id required")
	}
	if m.PluginVersion == "" {
		return fmt.Errorf("plugin_version required")
	}
	if m.GatewayCompatibility.APIContract != SupportedAPIContract {
		return fmt.Errorf("unsupported api_contract %q (want %s)", m.GatewayCompatibility.APIContract, SupportedAPIContract)
	}
	if m.Runtime.HandshakePath == "" || m.Runtime.HealthPath == "" {
		return fmt.Errorf("runtime handshake/health path required")
	}
	return nil
}
