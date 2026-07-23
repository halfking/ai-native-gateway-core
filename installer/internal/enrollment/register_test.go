package enrollment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegister_Success(t *testing.T) {
	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/instances/register" {
			t.Errorf("expected /api/v1/instances/register, got %s", r.URL.Path)
		}

		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if req.InstanceID == "" {
			t.Error("instance_id is required")
		}
		if req.LicenseKeyHash == "" {
			t.Error("license_key_hash is required")
		}

		resp := RegisterResponse{
			InstanceToken:   "test_instance_token",
			RefreshToken:    "test_refresh_token",
			ServerPublicKey: "test_server_pub_key",
			ExpiresAt:       time.Now().Add(7 * 24 * time.Hour),
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	req := RegisterRequest{
		InstanceID:     "test-instance-001",
		InstanceType:   "standalone",
		Hostname:       "test-host",
		IPAddress:      "192.168.1.100",
		Version:        "1.0.0",
		LicenseKeyHash: "hash123",
		HardwareHash:   "hw456",
		PublicKey:      "pubkey789",
	}

	resp, err := client.Register(context.Background(), req)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if resp.InstanceToken == "" {
		t.Error("expected non-empty instance_token")
	}
	if resp.RefreshToken == "" {
		t.Error("expected non-empty refresh_token")
	}
	if resp.ServerPublicKey == "" {
		t.Error("expected non-empty server_public_key")
	}
}

func TestRegister_DeviceLimitExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "device_limit_exceeded",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	req := RegisterRequest{
		InstanceID:     "test-instance-002",
		InstanceType:   "k8s-deployment",
		DeploymentID:   "default/my-deployment",
		ReplicaCount:   3,
		Hostname:       "test-host-2",
		IPAddress:      "192.168.1.101",
		Version:        "1.0.0",
		LicenseKeyHash: "hash123",
		HardwareHash:   "hw457",
		PublicKey:      "pubkey790",
	}

	_, err := client.Register(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for device limit exceeded")
	}

	errMsg := err.Error()
	if !containsString(errMsg, "409") && !containsString(errMsg, "device limit exceeded") {
		t.Errorf("expected 409 or device_limit_exceeded in error, got: %v", err)
	}
}

func TestRegister_K8sDeployment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if req.InstanceType != "k8s-deployment" {
			t.Errorf("expected instance_type=k8s-deployment, got %s", req.InstanceType)
		}
		if req.DeploymentID == "" {
			t.Error("deployment_id is required for k8s-deployment")
		}
		if req.ReplicaCount < 1 {
			t.Error("replica_count must be >= 1")
		}

		resp := RegisterResponse{
			InstanceToken:   "k8s_instance_token",
			RefreshToken:    "k8s_refresh_token",
			ServerPublicKey: "k8s_server_pub_key",
			ExpiresAt:       time.Now().Add(7 * 24 * time.Hour),
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	req := RegisterRequest{
		InstanceID:     "test-k8s-001",
		InstanceType:   "k8s-deployment",
		DeploymentID:   "kube-system/llm-gateway",
		ReplicaCount:   5,
		Hostname:       "llm-gateway-pod-1",
		IPAddress:      "10.244.0.10",
		Version:        "1.0.0",
		LicenseKeyHash: "hash_k8s",
		HardwareHash:   "hw_k8s",
		PublicKey:      "pubkey_k8s",
	}

	resp, err := client.Register(context.Background(), req)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if resp.InstanceToken == "" {
		t.Error("expected non-empty instance_token")
	}
}

// containsString 简单子串检查（2026-07-24 审计修复：原版一大段不可达代码 + json
// 反序列化副作用，导致 go vet 报 unreachable code；直接复用 strings.Contains）。
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
