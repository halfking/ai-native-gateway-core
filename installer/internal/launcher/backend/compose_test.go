package backend

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// skipIfNoDocker skips the test if docker isn't available or daemon isn't running.
func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon not running: %v", err)
	}
}

// TestComposeBackendStageHealthDrainRemove is an end-to-end integration test.
// It needs a mock image that responds 200 on /healthz. Set
// LAUNCHER_TEST_MOCK_IMAGE to enable. Without it, the test skips.
func TestComposeBackendStageHealthDrainRemove(t *testing.T) {
	skipIfNoDocker(t)
	mockImg := os.Getenv("LAUNCHER_TEST_MOCK_IMAGE")
	if mockImg == "" {
		t.Skip("set LAUNCHER_TEST_MOCK_IMAGE to run compose backend e2e")
	}

	b := NewComposeBackend(ComposeConfig{
		ProjectDir: t.TempDir(),
		GreenPort:  18783, // high port to avoid conflicts with real 8783
	})
	ctx := context.Background()

	addr, err := b.Stage(ctx, Release{Version: "test", Image: mockImg})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr) })

	if addr != "127.0.0.1:18783" {
		t.Fatalf("expected 127.0.0.1:18783, got %s", addr)
	}

	if err := b.Health(ctx, addr); err != nil {
		t.Fatalf("Health: %v", err)
	}

	if err := b.Drain(ctx, addr); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// After drain the container is stopped; Health should fail.
	if err := b.Health(ctx, addr); err == nil {
		t.Fatal("expected Health to fail after Drain")
	}
}

// TestComposeBackendStageMissingImage verifies Stage returns an error
// when docker can't pull/start the image (we use a non-existent image tag).
func TestComposeBackendStageMissingImage(t *testing.T) {
	skipIfNoDocker(t)
	b := NewComposeBackend(ComposeConfig{
		ProjectDir: t.TempDir(),
		GreenPort:  18784,
	})
	ctx := context.Background()
	_, err := b.Stage(ctx, Release{Version: "test", Image: "this-image-does-not-exist-12345:tag"})
	if err == nil {
		t.Fatal("expected error for non-existent image")
	}
}

// TestValidateImageTag verifies the allowlist regex used by Stage to
// prevent YAML injection via Release.Image. We exercise common valid
// forms and several injection / malformed attempts.
func TestValidateImageTag(t *testing.T) {
	cases := []struct {
		img   string
		valid bool
	}{
		{"registry/kx-gateway:v1.5.0", true},
		{"kx-gateway:latest", true},
		{"localhost:5000/myapp:1.0", true},
		// Injection attempts:
		{"img:v1\n  privileged: true", false},
		{"img:v1\n    volumes: [\"/:/host\"]", false},
		{"img", false},      // missing tag
		{"", false},         // empty
		{"$EVIL:v1", false}, // shell special
		{":v1", false},      // missing name
	}
	for _, c := range cases {
		err := validateImageTag(c.img)
		if c.valid && err != nil {
			t.Errorf("expected %q valid, got error: %v", c.img, err)
		}
		if !c.valid && err == nil {
			t.Errorf("expected %q rejected, got nil", c.img)
		}
	}
}
