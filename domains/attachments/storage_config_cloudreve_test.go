//go:build cloudreve_storage

package attachments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStorageConfig_CloudreveWiring exercises the public plumbing:
// ValidateStorageConfig → NewStorageBackendFromConfig → HealthCheck → LoadStorageConfigFromEnv.
//
// The test_server replies with a minimal but well-formed PROPFIND multistatus
// so the HealthCheck (which does PROPFIND Depth: 0) accepts the conversation.
func TestStorageConfig_CloudreveWiring(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(
			`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">` +
				`<D:response><D:href>/dav/llm-gateway-attachments</D:href>` +
				`<D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop>` +
				`<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>` +
				`</D:multistatus>`))
	}))
	defer ts.Close()

	cfg := StorageConfig{
		Type:                "cloudreve",
		CloudreveBaseURL:    ts.URL,
		CloudreveUsername:   "u",
		CloudrevePassword:   "p",
		CloudreveRemotePath: "/llm-gateway-attachments",
		CloudreveTimeoutSec: 10,
	}
	if err := ValidateStorageConfig(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	backend, err := NewStorageBackendFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewStorageBackendFromConfig: %v", err)
	}
	if backend.GetBackendType() != "cloudreve" {
		t.Errorf("type = %q", backend.GetBackendType())
	}
	if err := backend.HealthCheck(context.Background()); err != nil {
		t.Errorf("health: %v", err)
	}

	t.Setenv("LLM_GATEWAY_STORAGE_TYPE", "cloudreve")
	t.Setenv("LLM_GATEWAY_CLOUDREVE_BASE_URL", ts.URL)
	t.Setenv("LLM_GATEWAY_CLOUDREVE_USERNAME", "u")
	t.Setenv("LLM_GATEWAY_CLOUDREVE_PASSWORD", "p")
	t.Setenv("LLM_GATEWAY_CLOUDREVE_REMOTE_PATH", "/llm-gateway-attachments")
	t.Setenv("LLM_GATEWAY_CLOUDREVE_TIMEOUT_SEC", "10")

	loaded := LoadStorageConfigFromEnv()
	if loaded.Type != "cloudreve" {
		t.Errorf("loaded.Type = %q", loaded.Type)
	}
	if loaded.CloudreveBaseURL != ts.URL {
		t.Errorf("loaded.CloudreveBaseURL = %q", loaded.CloudreveBaseURL)
	}
	if loaded.CloudreveTimeoutSec != 10 {
		t.Errorf("loaded.CloudreveTimeoutSec = %d", loaded.CloudreveTimeoutSec)
	}
}

// TestStorageConfig_CloudreveValidate covers the validation matrix for the
// new cloudreve branch: each required field must trigger a descriptive error
// and a fully-populated config must pass.
func TestStorageConfig_CloudreveValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     StorageConfig
		wantErr string
	}{
		{"missing url", StorageConfig{Type: "cloudreve", CloudreveUsername: "u", CloudrevePassword: "p"}, "base url"},
		{"missing user", StorageConfig{Type: "cloudreve", CloudreveBaseURL: "http://x", CloudrevePassword: "p"}, "username"},
		{"missing pass", StorageConfig{Type: "cloudreve", CloudreveBaseURL: "http://x", CloudreveUsername: "u"}, "password"},
		{"all good", StorageConfig{Type: "cloudreve", CloudreveBaseURL: "http://x", CloudreveUsername: "u", CloudrevePassword: "p"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStorageConfig(tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
				t.Errorf("error = %q want contains %q", err, tc.wantErr)
			}
		})
	}
}
