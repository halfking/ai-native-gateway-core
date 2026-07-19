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
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+handshakePath, nil)
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
	if hs.Status != "ready" {
		return nil, fmt.Errorf("plugin not ready: %q", hs.Status)
	}
	return &hs, nil
}
