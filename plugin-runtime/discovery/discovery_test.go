package discovery

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDiscovery_Scan(t *testing.T) {
	// Create temp directory with test manifest
	tmpDir := t.TempDir()
	
	manifest := &Manifest{
		APIVersion: "llm-gateway.io/v1",
		Kind:       "Plugin",
		Metadata: Metadata{
			Name:        "test-plugin",
			Version:     "1.0.0",
			Description: "Test plugin",
			Author:      "test",
		},
		Spec: Spec{
			Type:       TypeExternal,
			Executable: "./bin/test-plugin",
			ExtensionPoints: []ExtensionPointDecl{
				{
					Name:      "test-extension",
					Phase:     "pre_upstream",
					Priority:  100,
					Interface: "RequestTransformer",
				},
			},
			Capabilities: Capabilities{},
			Resources:    ResourceLimits{},
		},
	}
	
	// Write manifest to temp directory
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)
	
	data, _ := yaml.Marshal(manifest)
	manifestPath := filepath.Join(pluginDir, "manifest.yaml")
	os.WriteFile(manifestPath, data, 0644)
	
	// Create discovery instance
	d, err := NewDiscovery(nil)
	if err != nil {
		t.Fatalf("failed to create discovery: %v", err)
	}
	defer d.Close()
	
	// Scan directory
	ctx := context.Background()
	manifests, err := d.Scan(ctx, []string{tmpDir})
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	
	// Verify results
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(manifests))
	}
	
	if manifests[0].Metadata.Name != "test-plugin" {
		t.Errorf("expected plugin name 'test-plugin', got %s", manifests[0].Metadata.Name)
	}
}

func TestDiscovery_Watch(t *testing.T) {
	tmpDir := t.TempDir()
	
	d, err := NewDiscovery(nil)
	if err != nil {
		t.Fatalf("failed to create discovery: %v", err)
	}
	defer d.Close()
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	events, err := d.Watch(ctx, []string{tmpDir})
	if err != nil {
		t.Fatalf("watch failed: %v", err)
	}
	
	// Create a new manifest
	go func() {
		time.Sleep(100 * time.Millisecond)
		
		manifest := &Manifest{
			APIVersion: "llm-gateway.io/v1",
			Kind:       "Plugin",
			Metadata: Metadata{
				Name:        "watched-plugin",
				Version:     "1.0.0",
				Description: "Watched plugin",
				Author:      "test",
			},
			Spec: Spec{
				Type:       TypeExternal,
				Executable: "./bin/watched",
				ExtensionPoints: []ExtensionPointDecl{
					{
						Name:      "test-ext",
						Phase:     "routing",
						Priority:  100,
						Interface: "RequestTransformer",
					},
				},
				Capabilities: Capabilities{},
				Resources:    ResourceLimits{},
			},
		}
		
		pluginDir := filepath.Join(tmpDir, "watched-plugin")
		os.MkdirAll(pluginDir, 0755)
		
		data, _ := yaml.Marshal(manifest)
		os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), data, 0644)
	}()
	
	// Wait for event
	select {
	case event := <-events:
		if event.Type != EventAdded {
			t.Errorf("expected EventAdded, got %s", event.Type)
		}
		if event.Manifest.Metadata.Name != "watched-plugin" {
			t.Errorf("expected plugin name 'watched-plugin', got %s", event.Manifest.Metadata.Name)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for event")
	}
}

func TestDiscovery_VerifySignature(t *testing.T) {
	// Generate test key pair
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	
	manifest := &Manifest{
		APIVersion: "llm-gateway.io/v1",
		Kind:       "Plugin",
		Metadata: Metadata{
			Name:        "signed-plugin",
			Version:     "1.0.0",
			Description: "Signed plugin",
			Author:      "test",
		},
		Spec: Spec{
			Type:       TypeExternal,
			Executable: "./bin/signed",
			ExtensionPoints: []ExtensionPointDecl{
				{
					Name:      "test",
					Phase:     "authentication",
					Priority:  100,
					Interface: "AuthenticationProvider",
				},
			},
			Capabilities: Capabilities{},
			Resources:    ResourceLimits{},
		},
	}
	
	// Sign manifest
	canonical, _ := yaml.Marshal(manifest)
	signature := ed25519.Sign(privKey, canonical)
	manifest.Signature = base64.StdEncoding.EncodeToString(signature)
	
	// Create discovery with public key
	d, err := NewDiscovery(pubKey)
	if err != nil {
		t.Fatalf("failed to create discovery: %v", err)
	}
	
	// Verify signature
	if err := d.VerifySignature(manifest); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}
	
	// Test with invalid signature
	manifest.Signature = "invalid-signature"
	if err := d.VerifySignature(manifest); err == nil {
		t.Error("expected verification to fail with invalid signature")
	}
}

func TestValidator_Validate(t *testing.T) {
	v := NewValidator()
	
	tests := []struct {
		name      string
		manifest  *Manifest
		expectErr bool
	}{
		{
			name: "valid manifest",
			manifest: &Manifest{
				APIVersion: "llm-gateway.io/v1",
				Kind:       "Plugin",
				Metadata: Metadata{
					Name:        "valid-plugin",
					Version:     "1.0.0",
					Description: "Valid plugin",
				},
				Spec: Spec{
					Type:       TypeExternal,
					Executable: "./bin/valid",
					ExtensionPoints: []ExtensionPointDecl{
						{
							Name:      "ext1",
							Phase:     "governance",
							Priority:  100,
							Interface: "RateLimiter",
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "missing name",
			manifest: &Manifest{
				APIVersion: "llm-gateway.io/v1",
				Kind:       "Plugin",
				Metadata: Metadata{
					Version:     "1.0.0",
					Description: "No name",
				},
				Spec: Spec{
					Type: TypeExternal,
					ExtensionPoints: []ExtensionPointDecl{
						{Name: "e", Phase: "routing", Interface: "R"},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "invalid version format",
			manifest: &Manifest{
				APIVersion: "llm-gateway.io/v1",
				Kind:       "Plugin",
				Metadata: Metadata{
					Name:        "invalid-version",
					Version:     "invalid",
					Description: "Bad version",
				},
				Spec: Spec{
					Type: TypeExternal,
					ExtensionPoints: []ExtensionPointDecl{
						{Name: "e", Phase: "routing", Interface: "R"},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "invalid phase",
			manifest: &Manifest{
				APIVersion: "llm-gateway.io/v1",
				Kind:       "Plugin",
				Metadata: Metadata{
					Name:        "invalid-phase",
					Version:     "1.0.0",
					Description: "Bad phase",
				},
				Spec: Spec{
					Type: TypeExternal,
					ExtensionPoints: []ExtensionPointDecl{
						{
							Name:      "ext",
							Phase:     "invalid_phase",
							Interface: "Interface",
						},
					},
				},
			},
			expectErr: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(tt.manifest)
			if (err != nil) != tt.expectErr {
				t.Errorf("expected error=%v, got error=%v", tt.expectErr, err)
			}
		})
	}
}
