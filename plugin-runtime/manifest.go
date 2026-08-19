package pluginruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m.ManifestPath = abs
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.PluginID == "" {
		return fmt.Errorf("plugin_id required")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`).MatchString(m.PluginID) {
		return fmt.Errorf("invalid plugin_id %q", m.PluginID)
	}
	if m.PluginVersion == "" {
		return fmt.Errorf("plugin_version required")
	}
	if m.GatewayCompatibility.APIContract != SupportedAPIContract {
		return fmt.Errorf("unsupported api_contract %q (want %s)", m.GatewayCompatibility.APIContract, SupportedAPIContract)
	}
	if m.Runtime.Protocol != "" && m.Runtime.Protocol != "http-unix-socket" {
		return fmt.Errorf("unsupported runtime protocol %q", m.Runtime.Protocol)
	}
	if m.Runtime.Entrypoint == "" {
		return fmt.Errorf("runtime entrypoint required")
	}
	if filepath.IsAbs(m.Runtime.Entrypoint) || strings.Contains(filepath.Clean(m.Runtime.Entrypoint), ".."+string(filepath.Separator)) || filepath.Clean(m.Runtime.Entrypoint) == ".." {
		return fmt.Errorf("runtime entrypoint must remain inside plugin directory")
	}
	if m.Runtime.HandshakePath == "" || m.Runtime.HealthPath == "" || !strings.HasPrefix(m.Runtime.HandshakePath, "/") || !strings.HasPrefix(m.Runtime.HealthPath, "/") {
		return fmt.Errorf("runtime handshake/health path required and must be absolute")
	}
	if m.Runtime.ShutdownGraceSecs < 0 || m.Runtime.ShutdownGraceSecs > 300 {
		return fmt.Errorf("runtime shutdown_grace_seconds must be between 0 and 300")
	}
	for _, binding := range m.Bindings {
		if err := ValidateBinding(m.PluginID, binding, BindingValidationOptions{}); err != nil {
			return err
		}
	}
	return nil
}
