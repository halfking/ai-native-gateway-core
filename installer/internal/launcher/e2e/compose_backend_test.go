//go:build e2e

// Real compose backend e2e tests — gated by the `e2e` build tag AND the
// LAUNCHER_TEST_MOCK_IMAGE env var (a Gateway-like image responding 200 on
// /healthz). These are the regression guards for audit findings C1/C2/C3:
// the ComposeBackend must drain/remove the instance identified by addr,
// not a fixed green project, and must give distinct addrs per Stage.
//
// Build the mock image:
//   echo 'FROM nginx:alpine
//   RUN printf "server{listen 8780;location /healthz{return 200 ok;}}" > /etc/nginx/conf.d/default.conf' \
//     | docker build -t launcher-mock-gateway:test -f - .
//   LAUNCHER_TEST_MOCK_IMAGE=launcher-mock-gateway:test \
//     go test -tags e2e ./internal/launcher/e2e/ -run TestCompose -v
package e2e

import (
	"context"
	"os"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/backend"
)

func skipNoMockImage(t *testing.T) (string, bool) {
	t.Helper()
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker socket not accessible")
	}
	img := os.Getenv("LAUNCHER_TEST_MOCK_IMAGE")
	if img == "" {
		t.Skip("set LAUNCHER_TEST_MOCK_IMAGE to run real compose e2e")
	}
	return img, true
}

// TestComposeDrainTargetsAddr (audit C1 regression): Drain(blueAddr) must
// stop the instance at blueAddr, NOT the green instance. The pre-fix
// ComposeBackend ignored addr and always ran `docker compose -p
// kxgw-green-8783 stop`, so Apply's drain-blue call would kill the just-
// activated green. This test stages two instances, drains the first, and
// asserts the second is still healthy.
func TestComposeDrainTargetsAddr(t *testing.T) {
	img, ok := skipNoMockImage(t)
	if !ok {
		return
	}
	b := backend.NewComposeBackend(backend.ComposeConfig{
		ProjectDir:    t.TempDir(),
		GreenPortBase: 18900,
	})
	ctx := context.Background()

	addr1, err := b.Stage(ctx, backend.Release{Version: "v1", Image: img})
	if err != nil {
		t.Fatalf("Stage 1: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr1) })
	addr2, err := b.Stage(ctx, backend.Release{Version: "v2", Image: img})
	if err != nil {
		t.Fatalf("Stage 2: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr2) })

	// Both healthy initially.
	if err := b.Health(ctx, addr1); err != nil {
		t.Fatalf("Health 1: %v", err)
	}
	if err := b.Health(ctx, addr2); err != nil {
		t.Fatalf("Health 2: %v", err)
	}

	// Drain addr1 (simulating Apply draining blue). addr2 (the active
	// green) must stay up.
	if err := b.Drain(ctx, addr1); err != nil {
		t.Fatalf("Drain addr1: %v", err)
	}
	if err := b.Health(ctx, addr2); err != nil {
		t.Fatalf("C1 REGRESSION: Drain(%q) killed addr2 (%v) — Drain must target only the addr it was given", addr1, addr2)
	}
	// addr1 should now be down.
	if err := b.Health(ctx, addr1); err == nil {
		t.Fatalf("addr1 should be stopped after Drain")
	}
}

// TestComposeRemoveTargetsAddr (audit C2 regression): Remove(blueAddr) must
// remove the instance at blueAddr, NOT the active green. The pre-fix
// retained-remove (1h post-Apply) would destroy the active green.
func TestComposeRemoveTargetsAddr(t *testing.T) {
	img, ok := skipNoMockImage(t)
	if !ok {
		return
	}
	b := backend.NewComposeBackend(backend.ComposeConfig{
		ProjectDir:    t.TempDir(),
		GreenPortBase: 19000,
	})
	ctx := context.Background()

	addr1, err := b.Stage(ctx, backend.Release{Version: "v1", Image: img})
	if err != nil {
		t.Fatalf("Stage 1: %v", err)
	}
	addr2, err := b.Stage(ctx, backend.Release{Version: "v2", Image: img})
	if err != nil {
		t.Fatalf("Stage 2: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr2) })

	// Remove addr1 (simulating retained-remove of blue). addr2 must stay.
	if err := b.Remove(ctx, addr1); err != nil {
		t.Fatalf("Remove addr1: %v", err)
	}
	if err := b.Health(ctx, addr2); err != nil {
		t.Fatalf("C2 REGRESSION: Remove(%q) killed addr2 (%v)", addr1, addr2)
	}
}

// TestComposeTwoStagesDistinctAddr (audit C3 regression): two Stage calls
// must produce distinct ports/instances so a second Prepare doesn't
// recreate the live container. Pre-fix the port was fixed at 8783.
func TestComposeTwoStagesDistinctAddr(t *testing.T) {
	img, ok := skipNoMockImage(t)
	if !ok {
		return
	}
	b := backend.NewComposeBackend(backend.ComposeConfig{
		ProjectDir:    t.TempDir(),
		GreenPortBase: 19100,
	})
	ctx := context.Background()

	addr1, err := b.Stage(ctx, backend.Release{Version: "v1", Image: img})
	if err != nil {
		t.Fatalf("Stage 1: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr1) })

	addr2, err := b.Stage(ctx, backend.Release{Version: "v2", Image: img})
	if err != nil {
		t.Fatalf("Stage 2: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, addr2) })

	if addr1 == addr2 {
		t.Fatalf("C3 REGRESSION: two Stages returned same addr %s — both greens would collide on the active instance", addr1)
	}
	// Both coexist.
	if err := b.Health(ctx, addr1); err != nil {
		t.Fatalf("Health 1: %v", err)
	}
	if err := b.Health(ctx, addr2); err != nil {
		t.Fatalf("Health 2: %v", err)
	}
}
