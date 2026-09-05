package upgrader

import (
	"context"
	"encoding/json"
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

func TestClient_CheckDistributionSendsProofForInstanceHint(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maintain-api/distribution/version-check" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("instance_id"); got != proof.InstanceID {
			t.Fatalf("instance_id = %q, want %q", got, proof.InstanceID)
		}
		assertProofHeaders(t, r, proof)
		_, _ = w.Write([]byte(`{"update_available":true,"latest_version":"v1.15.0","auto_upgrade":true,"upgrade_policy_id":"policy-1","estimated_downtime_minutes":10,"target_artifacts":[{"platform":"linux","arch":"amd64","artifact_name":"gateway.tar.gz","size_bytes":123,"sha256":"abc","storage_uri":"https://files.example/gateway.tar.gz"}]}`))
	}))
	defer server.Close()

	result, err := NewClientWithProof(server.URL, proof).CheckDistribution(context.Background(), "v1.14.0", "stable", "linux", "amd64")
	if err != nil {
		t.Fatalf("CheckDistribution() error = %v", err)
	}
	if !result.HasUpdate || result.Release == nil || result.Release.Version != "v1.15.0" {
		t.Fatalf("unexpected release result: %#v", result)
	}
	if !result.AutoUpgrade || result.UpgradePolicyID != "policy-1" || result.EstimatedDowntimeMinutes != 10 {
		t.Fatalf("unexpected policy hint: %#v", result)
	}
}

func TestClient_CheckDistributionWithoutOrRejectedProofDoesNotCreateHint(t *testing.T) {
	tests := []struct {
		name  string
		proof DeviceProof
	}{
		{name: "anonymous"},
		{name: "cross instance proof rejected by server", proof: DeviceProof{InstanceID: "instance-2", LicenseKey: "wrong", HardwareHash: "wrong"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.proof.valid() {
					assertProofHeaders(t, r, tt.proof)
					if got := r.URL.Query().Get("instance_id"); got != tt.proof.InstanceID {
						t.Fatalf("instance_id = %q, want %q", got, tt.proof.InstanceID)
					}
				} else if got := r.URL.Query().Get("instance_id"); got != "" {
					t.Fatalf("anonymous request included instance_id %q", got)
				}
				_, _ = w.Write([]byte(`{"update_available":true,"latest_version":"v1.15.0","auto_upgrade":false,"target_artifacts":[{"platform":"linux","arch":"amd64","sha256":"abc","storage_uri":"https://files.example/gateway.tar.gz"}]}`))
			}))
			defer server.Close()

			result, err := NewClientWithProof(server.URL, tt.proof).CheckDistribution(context.Background(), "v1.14.0", "stable", "linux", "amd64")
			if err != nil {
				t.Fatalf("CheckDistribution() error = %v", err)
			}
			if !result.HasUpdate || result.AutoUpgrade || result.UpgradePolicyID != "" {
				t.Fatalf("unexpected anonymous/rejected-proof result: %#v", result)
			}
		})
	}
}

func TestClient_PollUpgradeTaskTreatsConsecutive204AsIdle(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/maintain-api/upgrade/poll" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		assertProofHeaders(t, r, proof)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClientWithProof(server.URL, proof)
	for i := 0; i < 2; i++ {
		task, err := client.PollUpgradeTask(context.Background())
		if err != nil {
			t.Fatalf("poll %d error = %v", i, err)
		}
		if task != nil {
			t.Fatalf("poll %d task = %#v, want nil", i, task)
		}
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestClient_P3TaskProtocol(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/maintain-api/upgrade/poll":
			if r.Method != http.MethodGet {
				t.Fatalf("poll method = %s", r.Method)
			}
			assertProofHeaders(t, r, proof)
			_, _ = w.Write([]byte(`{"task_id":42,"to_version":"v1.15.0","pre_check":true,"download_hint":"POST ticket"}`))
		case "/maintain-api/downloads/ticket":
			if r.Method != http.MethodPost {
				t.Fatalf("ticket method = %s", r.Method)
			}
			var body downloadTicketRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode ticket request: %v", err)
			}
			if body.Version != "v1.15.0" || body.Platform != "linux" || body.Arch != "amd64" {
				t.Fatalf("unexpected ticket request: %#v", body)
			}
			_, _ = w.Write([]byte(`{"request_id":"req-1","url":"https://files.example/gateway.tar.gz","expires_at":"2026-08-17T12:00:00Z","file_name":"gateway.tar.gz"}`))
		case "/maintain-api/upgrade/tasks/42/progress":
			assertProofHeaders(t, r, proof)
			var body UpgradeProgress
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode progress: %v", err)
			}
			if body.Status != "downloading" || body.Stage != "download" || body.Progress != 50 {
				t.Fatalf("unexpected progress: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
		case "/maintain-api/upgrade/tasks/42/result":
			assertProofHeaders(t, r, proof)
			var body UpgradeResult
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if body.Status != "completed" || body.DurationSeconds != 12 {
				t.Fatalf("unexpected result: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithProof(server.URL, proof)
	task, err := client.PollUpgradeTask(context.Background())
	if err != nil {
		t.Fatalf("PollUpgradeTask() error = %v", err)
	}
	if task.TaskID != 42 || task.ToVersion != "v1.15.0" {
		t.Fatalf("unexpected task: %#v", task)
	}
	ticket, err := client.CreateDownloadTicket(context.Background(), task.ToVersion, "linux", "amd64")
	if err != nil {
		t.Fatalf("CreateDownloadTicket() error = %v", err)
	}
	if ticket.RequestID != "req-1" || ticket.URL == "" {
		t.Fatalf("unexpected ticket: %#v", ticket)
	}
	if err := client.ReportUpgradeProgress(context.Background(), task.TaskID, UpgradeProgress{Status: "downloading", Stage: "download", Progress: 50}); err != nil {
		t.Fatalf("ReportUpgradeProgress() error = %v", err)
	}
	if err := client.ReportUpgradeResult(context.Background(), task.TaskID, UpgradeResult{Status: "completed", DurationSeconds: 12}); err != nil {
		t.Fatalf("ReportUpgradeResult() error = %v", err)
	}
}

func TestClient_P3TaskAPIsRequireProof(t *testing.T) {
	client := NewClient("https://maintain.example")
	if _, err := client.PollUpgradeTask(context.Background()); err == nil {
		t.Fatal("PollUpgradeTask() error = nil, want proof error")
	}
	if err := client.ReportUpgradeProgress(context.Background(), 1, UpgradeProgress{Status: "downloading", Stage: "download"}); err == nil {
		t.Fatal("ReportUpgradeProgress() error = nil, want proof error")
	}
	if err := client.ReportUpgradeResult(context.Background(), 1, UpgradeResult{Status: "completed"}); err == nil {
		t.Fatal("ReportUpgradeResult() error = nil, want proof error")
	}
}

func TestClient_CheckDistributionEscapesQueryValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("channel"); got != "beta&canary" {
			t.Fatalf("channel = %q", got)
		}
		if got := r.URL.Query().Get("current"); got != "v1.14.0+build?x=1" {
			t.Fatalf("current = %q", got)
		}
		_, _ = w.Write([]byte(`{"update_available":false}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL).CheckDistribution(context.Background(), "v1.14.0+build?x=1", "beta&canary", "linux", "amd64")
	if err != nil {
		t.Fatalf("CheckDistribution() error = %v", err)
	}
	if result.HasUpdate {
		t.Fatal("HasUpdate = true, want false")
	}
}

func TestClient_RejectsInvalidTaskStatuses(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	client := NewClientWithProof("https://maintain.example", proof)
	if err := client.ReportUpgradeProgress(context.Background(), 1, UpgradeProgress{Status: "completed", Stage: "download"}); err == nil {
		t.Fatal("ReportUpgradeProgress() error = nil, want invalid status")
	}
	if err := client.ReportUpgradeResult(context.Background(), 1, UpgradeResult{Status: "installing"}); err == nil {
		t.Fatal("ReportUpgradeResult() error = nil, want invalid status")
	}
}

func assertProofHeaders(t *testing.T, r *http.Request, proof DeviceProof) {
	t.Helper()
	if got := r.Header.Get("X-Instance-ID"); got != proof.InstanceID {
		t.Errorf("X-Instance-ID = %q, want %q", got, proof.InstanceID)
	}
	if got := r.Header.Get("X-License-Key"); got != proof.LicenseKey {
		t.Errorf("X-License-Key = %q, want %q", got, proof.LicenseKey)
	}
	if got := r.Header.Get("X-Hardware-Hash"); got != proof.HardwareHash {
		t.Errorf("X-Hardware-Hash = %q, want %q", got, proof.HardwareHash)
	}
}
