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
