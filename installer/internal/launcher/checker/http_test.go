package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/upgrader"
)

func TestMasterHTTPSourceUsesMaintainVersionCheckAndProof(t *testing.T) {
	proof := upgrader.DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maintain-api/distribution/version-check" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("current") != "v1.14.0" || query.Get("channel") != "stable" || query.Get("platform") != "linux" || query.Get("arch") != "arm64" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		if query.Get("instance_id") != proof.InstanceID {
			t.Fatalf("instance_id = %q", query.Get("instance_id"))
		}
		if got := r.Header.Get("X-Instance-ID"); got != proof.InstanceID {
			t.Fatalf("X-Instance-ID = %q", got)
		}
		if got := r.Header.Get("X-License-Key"); got != proof.LicenseKey {
			t.Fatalf("X-License-Key = %q", got)
		}
		if got := r.Header.Get("X-Hardware-Hash"); got != proof.HardwareHash {
			t.Fatalf("X-Hardware-Hash = %q", got)
		}
		_, _ = w.Write([]byte(`{"update_available":true,"latest_version":"v1.15.0","auto_upgrade":true,"upgrade_policy_id":"policy-1","estimated_downtime_minutes":8,"target_artifacts":[{"platform":"linux","arch":"arm64","sha256":"abc","storage_uri":"https://files.example/gateway.tar.gz"}]}`))
	}))
	defer server.Close()

	source := MasterHTTPSource{
		MasterURL:      server.URL,
		CurrentVersion: func() string { return "v1.14.0" },
		Channel:        "stable",
		Platform:       "linux",
		Arch:           "arm64",
		Proof:          proof,
	}
	release, err := source.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if release == nil || release.Version != "v1.15.0" || !release.AutoUpgrade || release.UpgradePolicyID != "policy-1" || release.EstimatedDowntimeMinutes != 8 {
		t.Fatalf("unexpected release: %#v", release)
	}
}

func TestMasterHTTPSourceTreats204AsNoRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	release, err := (&MasterHTTPSource{MasterURL: server.URL}).Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if release != nil {
		t.Fatalf("release = %#v, want nil", release)
	}
}
