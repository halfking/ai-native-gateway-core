package pluginapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestListPluginsHandler(t *testing.T) {
	// Create temporary plugin directory
	tmpDir := t.TempDir()
	
	// Create test plugin manifests
	createTestPlugin(t, tmpDir, "test-plugin-1", "Test Plugin 1", "1.0.0")
	createTestPlugin(t, tmpDir, "test-plugin-2", "Test Plugin 2", "2.0.0")

	handler := NewPluginAPIHandler(tmpDir)

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		expectedCount  int
	}{
		{
			name:           "list all plugins",
			queryParams:    "",
			expectedStatus: http.StatusOK,
			expectedCount:  2,
		},
		{
			name:           "filter enabled plugins",
			queryParams:    "?enabled=true",
			expectedStatus: http.StatusOK,
			expectedCount:  2,
		},
		{
			name:           "sort by plugin_id",
			queryParams:    "?sort=plugin_id",
			expectedStatus: http.StatusOK,
			expectedCount:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins"+tt.queryParams, nil)
			w := httptest.NewRecorder()

			handler.ListPluginsHandler(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var response PluginListResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if !response.Success {
				t.Errorf("expected success=true, got false: %s", response.Error)
			}

			if response.Total != tt.expectedCount {
				t.Errorf("expected %d plugins, got %d", tt.expectedCount, response.Total)
			}
		})
	}
}

func TestGetPluginHandler(t *testing.T) {
	tmpDir := t.TempDir()
	createTestPlugin(t, tmpDir, "test-plugin", "Test Plugin", "1.0.0")

	handler := NewPluginAPIHandler(tmpDir)

	tests := []struct {
		name           string
		pluginID       string
		expectedStatus int
		expectError    bool
	}{
		{
			name:           "get existing plugin",
			pluginID:       "test-plugin",
			expectedStatus: http.StatusOK,
			expectError:    false,
		},
		{
			name:           "get non-existing plugin",
			pluginID:       "non-existing",
			expectedStatus: http.StatusNotFound,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/"+tt.pluginID, nil)
			w := httptest.NewRecorder()

			handler.GetPluginHandler(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var response PluginListResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if tt.expectError && response.Success {
				t.Error("expected error but got success")
			}

			if !tt.expectError && !response.Success {
				t.Errorf("expected success but got error: %s", response.Error)
			}
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	handler := NewPluginAPIHandler(t.TempDir())

	tests := []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{
			name:    "POST to list endpoint",
			method:  http.MethodPost,
			handler: handler.ListPluginsHandler,
		},
		{
			name:    "DELETE to get endpoint",
			method:  http.MethodDelete,
			handler: handler.GetPluginHandler,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/plugins", nil)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
			}
		})
	}
}

// Helper function to create test plugin
func createTestPlugin(t *testing.T, baseDir, pluginID, displayName, version string) {
	t.Helper()

	pluginDir := filepath.Join(baseDir, pluginID)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("failed to create plugin dir: %v", err)
	}

	manifest := &pluginruntime.Manifest{
		SchemaVersion: 2,
		PluginID:      pluginID,
		DisplayName:   displayName,
		PluginVersion: version,
		BuildSeq:      1,
		Channel:       "stable",
		GatewayCompatibility: pluginruntime.GatewayCompatibility{
			APIContract: "gateway-plugin-v2",
		},
		Runtime: pluginruntime.Runtime{
			Entrypoint:          "bin/plugin",
			Protocol:            "http-unix-socket",
			HealthPath:          "/plugin/healthz",
			HandshakePath:       "/plugin/handshake",
			ShutdownGraceSecs:   5,
		},
		Capabilities: []string{"request.observe"},
		Web: pluginruntime.Web{
			Mount:    "iframe",
			BasePath: "/plugins/" + pluginID + "/",
			Entry:    "web/index.html",
		},
		Activation: pluginruntime.Activation{
			ModuleKey: pluginID,
		},
	}

	manifestPath := filepath.Join(pluginDir, "plugin-manifest.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}
}
