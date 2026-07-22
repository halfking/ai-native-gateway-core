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
//
// Audit C1/C2 regression coverage: this test now verifies Drain and Remove
// target the instance at the *returned addr* (derived from its port), not a
// fixed green project. The previous implementation ignored addr and always
// operated on the fixed green port — so Apply's drain-blue call would stop
// the just-activated green. With the addr-based mapping, Drain(addr) stops
// exactly the container at that addr.
func TestComposeBackendStageHealthDrainRemove(t *testing.T) {
	skipIfNoDocker(t)
	mockImg := os.Getenv("LAUNCHER_TEST_MOCK_IMAGE")
	if mockImg == "" {
		t.Skip("set LAUNCHER_TEST_MOCK_IMAGE to run compose backend e2e")
	}

	b := NewComposeBackend(ComposeConfig{
		ProjectDir:    t.TempDir(),
		GreenPortBase: 18783, // high range to avoid conflicts with real 8783
	})
	ctx := context.Background()

	addr, err := b.Stage(ctx, Release{Version: "test", Image: mockImg})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr) })

	// Stage returns a real port (not hardcoded 18783 — the backend picks
	// the first free port from GreenPortBase). Just verify it's in range.
	port := portFromAddr(addr)
	if port < 18783 || port >= 18983 {
		t.Fatalf("expected port in [18783,18983), got %s", addr)
	}

	if err := b.Health(ctx, addr); err != nil {
		t.Fatalf("Health: %v", err)
	}

	// Drain must target THIS instance (by port from addr), not a fixed
	// project. After Drain, this addr's Health should fail.
	if err := b.Drain(ctx, addr); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := b.Health(ctx, addr); err == nil {
		t.Fatal("expected Health to fail after Drain of this addr")
	}
}

// TestComposeBackendTwoGreensCoexist (audit C3 regression): two Stage calls
// must produce two distinct addrs/ports so a second green doesn't recreate
// the first (which may be the active instance mid-cycle). Requires Docker.
func TestComposeBackendTwoGreensCoexist(t *testing.T) {
	skipIfNoDocker(t)
	mockImg := os.Getenv("LAUNCHER_TEST_MOCK_IMAGE")
	if mockImg == "" {
		t.Skip("set LAUNCHER_TEST_MOCK_IMAGE to run compose backend e2e")
	}
	b := NewComposeBackend(ComposeConfig{
		ProjectDir:    t.TempDir(),
		GreenPortBase: 18800,
	})
	ctx := context.Background()

	addr1, err := b.Stage(ctx, Release{Version: "v1", Image: mockImg})
	if err != nil {
		t.Fatalf("Stage 1: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr1) })

	addr2, err := b.Stage(ctx, Release{Version: "v2", Image: mockImg})
	if err != nil {
		t.Fatalf("Stage 2: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr2) })

	if addr1 == addr2 {
		t.Fatalf("two Stage calls returned same addr %s — port not unique", addr1)
	}
	// Both should be healthy simultaneously (coexist).
	if err := b.Health(ctx, addr1); err != nil {
		t.Fatalf("Health 1 after second Stage: %v", err)
	}
	if err := b.Health(ctx, addr2); err != nil {
		t.Fatalf("Health 2: %v", err)
	}
}

// TestComposeBackendStageMissingImage verifies Stage returns an error
// when docker can't pull/start the image.
func TestComposeBackendStageMissingImage(t *testing.T) {
	skipIfNoDocker(t)
	b := NewComposeBackend(ComposeConfig{
		ProjectDir:    t.TempDir(),
		GreenPortBase: 18784,
	})
	ctx := context.Background()
	_, err := b.Stage(ctx, Release{Version: "test", Image: "this-image-does-not-exist-12345:tag"})
	if err == nil {
		t.Fatal("expected error for non-existent image")
	}
}

// TestComposeBackendDrainBadAddr (audit C1): Drain with an unparseable
// addr must return an error, not silently operate on a default project.
func TestComposeBackendDrainBadAddr(t *testing.T) {
	b := NewComposeBackend(ComposeConfig{ProjectDir: t.TempDir(), GreenPortBase: 8783})
	for _, bad := range []string{"", "noport", "127.0.0.1:", ":notaport"} {
		if err := b.Drain(context.Background(), bad); err == nil {
			t.Errorf("Drain(%q) returned nil, expected error", bad)
		}
		if err := b.Remove(context.Background(), bad); err == nil {
			t.Errorf("Remove(%q) returned nil, expected error", bad)
		}
	}
}

// TestValidateImageTag verifies the allowlist regex used by Stage to
// prevent YAML injection via Release.Image.
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

func TestValidateEnvPath(t *testing.T) {
	cases := []struct {
		path  string
		valid bool
	}{
		{"", true}, // empty = disabled, valid
		{"/etc/kx-gateway/env", true},
		{"/etc/kx-gateway/env-file.env", true},
		// Injection / invalid:
		{"relative/path", false},      // not absolute
		{"\n/etc/x", false},           // newline
		{"/etc/x; rm -rf /", false},   // shell meta
		{"/etc/x with space", false},  // space
	}
	for _, c := range cases {
		err := validateEnvPath(c.path)
		if c.valid && err != nil {
			t.Errorf("expected %q valid, got error: %v", c.path, err)
		}
		if !c.valid && err == nil {
			t.Errorf("expected %q rejected, got nil", c.path)
		}
	}
}
