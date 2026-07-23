package upgrader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_CheckUpdate(t *testing.T) {
	tests := []struct {
		name           string
		currentVersion string
		channel        string
		responseStatus int
		responseBody   string
		wantErr        bool
		wantVersion    string
	}{
		{
			name:           "update available",
			currentVersion: "v1.12.0",
			channel:        "stable",
			responseStatus: http.StatusOK,
			responseBody: `{
				"has_update": true,
				"release": {
					"version": "v1.13.0",
					"download_url": "https://example.com/v1.13.0.tar.gz",
					"sha256": "abc123",
					"changelog": "New features"
				}
			}`,
			wantErr:     false,
			wantVersion: "v1.13.0",
		},
		{
			name:           "no update available",
			currentVersion: "v1.13.0",
			channel:        "stable",
			responseStatus: http.StatusOK,
			responseBody: `{
				"has_update": false
			}`,
			wantErr:     false,
			wantVersion: "",
		},
		{
			name:           "server error",
			currentVersion: "v1.12.0",
			channel:        "stable",
			responseStatus: http.StatusInternalServerError,
			responseBody:   `{"error":"internal error"}`,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/updates/latest" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.responseStatus)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			client := NewClient(server.URL)
			ctx := context.Background()

			resp, err := client.CheckUpdate(ctx, tt.currentVersion, tt.channel)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckUpdate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && resp != nil && resp.HasUpdate && resp.Release.Version != tt.wantVersion {
				t.Errorf("CheckUpdate() version = %v, want %v", resp.Release.Version, tt.wantVersion)
			}
		})
	}
}

func TestClient_CheckUpdateDistribution(t *testing.T) {
	tests := []struct {
		name         string
		current      string
		channel      string
		platform     string
		arch         string
		responseBody string
		wantErr      bool
		wantVersion  string
		wantURL      string
	}{
		{
			name:     "update available maps to Release",
			current:  "v1.12.0",
			channel:  "stable",
			platform: "linux",
			arch:     "amd64",
			responseBody: `{
				"update_available": true,
				"latest_version": "v1.14.0",
				"mandatory": false,
				"target_artifacts": [
					{"platform":"linux","arch":"amd64","edition":"customer",
					 "filename":"llm-gateway-go-v1.14.0-linux-amd64-offline.tar.gz",
					 "size":12345,"sha256":"deadbeef",
					 "storage_uri":"https://files.kxpms.cn/d/f1/llm-gateway-go.tar.gz"}
				]
			}`,
			wantVersion: "v1.14.0",
			wantURL:     "https://files.kxpms.cn/d/f1/llm-gateway-go.tar.gz",
		},
		{
			name:     "no update available",
			current:  "v1.14.0",
			channel:  "stable",
			platform: "linux",
			arch:     "amd64",
			responseBody: `{
				"update_available": false,
				"latest_version": "v1.14.0"
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/maintain-api/distribution/version-check" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				q := r.URL.Query()
				if got := q.Get("channel"); got != tt.channel {
					t.Errorf("channel = %s, want %s", got, tt.channel)
				}
				if got := q.Get("current"); got != tt.current {
					t.Errorf("current = %s, want %s", got, tt.current)
				}
				if got := q.Get("platform"); got != tt.platform {
					t.Errorf("platform = %s, want %s", got, tt.platform)
				}
				if got := q.Get("arch"); got != tt.arch {
					t.Errorf("arch = %s, want %s", got, tt.arch)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			client := NewClient(server.URL)
			resp, err := client.CheckUpdateDistribution(context.Background(), tt.current, tt.channel, tt.platform, tt.arch)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckUpdateDistribution err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantVersion == "" {
				if resp.HasUpdate {
					t.Errorf("HasUpdate = true, want false")
				}
				return
			}
			if !resp.HasUpdate {
				t.Fatalf("HasUpdate = false, want true")
			}
			if resp.Release == nil {
				t.Fatalf("Release nil")
			}
			if resp.Release.Version != tt.wantVersion {
				t.Errorf("Version = %s, want %s", resp.Release.Version, tt.wantVersion)
			}
			if resp.Release.DownloadURL != tt.wantURL {
				t.Errorf("DownloadURL = %s, want %s", resp.Release.DownloadURL, tt.wantURL)
			}
		})
	}
}
