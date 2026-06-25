package identity

import (
	"context"
	"net/http"
	"testing"
)

func TestNewIdentityBuilder_DefaultProfileEmpty(t *testing.T) {
	b := NewIdentityBuilder()
	if b == nil {
		t.Fatal("NewIdentityBuilder returned nil")
	}
	if b.defaultProfile != "" {
		t.Fatalf("default profile should be empty by default, got %q", b.defaultProfile)
	}
}

func TestIdentityBuilder_WithDefaultProfile(t *testing.T) {
	b := NewIdentityBuilder().WithDefaultProfile("roocode")
	if b.defaultProfile != "roocode" {
		t.Fatalf("WithDefaultProfile did not set field, got %q", b.defaultProfile)
	}
}

func TestIdentityBuilder_BuildNoRequest(t *testing.T) {
	b := NewIdentityBuilder()
	ident, err := b.Build(context.Background(), "tenant-x")
	if err != nil {
		t.Fatalf("Build without request returned error: %v", err)
	}
	if ident.TenantID != "tenant-x" {
		t.Errorf("expected tenant tenant-x, got %q", ident.TenantID)
	}
	if ident.IdentityHash == "" {
		t.Fatal("expected non-empty identity hash")
	}
}

func TestIdentityBuilder_BuildWithRequest(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "from-ctx")
	ctx := WithRequest(context.Background(), r)

	b := NewIdentityBuilder().WithDefaultProfile("roocode")
	ident, err := b.Build(ctx, "default")
	if err != nil {
		t.Fatalf("Build with request returned error: %v", err)
	}
	if ident.Fingerprint.DeviceSeed != "from-ctx" {
		t.Errorf("expected device seed from request, got %q", ident.Fingerprint.DeviceSeed)
	}
	if ident.Fingerprint.ClientProfile != "roocode" {
		t.Errorf("expected default profile, got %q", ident.Fingerprint.ClientProfile)
	}
}

func TestIdentityBuilder_BuildWithEmptyRequest(t *testing.T) {
	// 显式 nil *http.Request —— 走兜底分支
	ctx := WithRequest(context.Background(), nil)
	b := NewIdentityBuilder()
	ident, err := b.Build(ctx, "default")
	if err != nil {
		t.Fatalf("Build with nil request returned error: %v", err)
	}
	if ident.TenantID != "default" {
		t.Errorf("expected tenant default, got %q", ident.TenantID)
	}
}
