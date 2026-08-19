package pluginruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Handshake 对插件调用 GET {base}{handshakePath}，校验 api_contract 与 plugin_id，
// 并在 status==ready 时返回握手响应。
func Handshake(client *http.Client, base, handshakePath string, expected *Manifest) (*HandshakeResponse, error) {
	return HandshakeContext(context.Background(), client, base, handshakePath, expected)
}

// HandshakeContext is the cancellation-aware readiness handshake used by startup.
func HandshakeContext(ctx context.Context, client *http.Client, base, handshakePath string, expected *Manifest) (*HandshakeResponse, error) {
	if expected == nil {
		return nil, fmt.Errorf("handshake expected manifest required")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+handshakePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("handshake request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("handshake status %d", resp.StatusCode)
	}
	var hs HandshakeResponse
	if err := json.NewDecoder(resp.Body).Decode(&hs); err != nil {
		return nil, fmt.Errorf("decode handshake: %w", err)
	}
	if hs.APIContract != SupportedAPIContract {
		return nil, fmt.Errorf("api_contract mismatch: %q", hs.APIContract)
	}
	if hs.PluginID != expected.PluginID {
		return nil, fmt.Errorf("plugin_id mismatch: got %q want %q", hs.PluginID, expected.PluginID)
	}
	if expected.PluginVersion != "" && hs.PluginVersion != "" && hs.PluginVersion != expected.PluginVersion {
		return nil, fmt.Errorf("plugin_version mismatch: got %q want %q", hs.PluginVersion, expected.PluginVersion)
	}
	if len(expected.Capabilities) > 0 {
		allowed := make(map[string]struct{}, len(expected.Capabilities))
		for _, capability := range expected.Capabilities {
			allowed[capability] = struct{}{}
		}
		for _, capability := range hs.Capabilities {
			if _, ok := allowed[capability]; !ok {
				return nil, fmt.Errorf("handshake capability %q not declared by manifest", capability)
			}
		}
	}
	return &hs, nil
}
