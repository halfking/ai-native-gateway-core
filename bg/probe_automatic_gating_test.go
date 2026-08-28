package bg

import (
	"context"
	"strings"
	"testing"
)

func TestAutomaticProbeEligibilitySQLGuardsCredentialAndProviderState(t *testing.T) {
	q := &ProbeQueue{}
	// Keep the public behavior contract close to the queue implementation: the
	// check must reject every disabled credential/provider state while manual
	// tasks remain outside this query.
	// The SQL is exercised by integration tests; this focused guard prevents a
	// future edit from dropping one of the required lifecycle gates.
	// (The query itself is emitted by automaticTaskEligible.)
	_ = q
	for _, want := range []string{
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.enabled, FALSE) = TRUE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
	} {
		if !strings.Contains(automaticProbeEligibilitySQL(), want) {
			t.Fatalf("automatic eligibility SQL missing %q", want)
		}
	}
}

func TestProbeQueueAutomaticEligibilitySQLIsSharedShape(t *testing.T) {
	for _, want := range []string{"credentials c", "JOIN providers p ON p.id = c.provider_id", "c.id = $1"} {
		if !strings.Contains(automaticProbeEligibilitySQL(), want) {
			t.Fatalf("automatic eligibility SQL missing %q", want)
		}
	}
}

func TestProbeServiceManualTaskBypassesAutomaticEligibility(t *testing.T) {
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true},
		func(_ context.Context, _ probeOutcome) {},
	)
	called := false
	service.automaticEligibilityFn = func(context.Context, ProbeQueueTask) (bool, error) {
		called = true
		return false, nil
	}
	// Manual tasks must not consult the automatic gate. This test only checks
	// the boundary; the full probe path is covered by existing service tests.
	if _, err := service.Run(context.Background(), ProbeQueueTask{CredentialID: 1, RawModel: "m"}); err != nil {
		t.Fatalf("manual task failed: %v", err)
	}
	if called {
		t.Fatal("manual task consulted automatic eligibility")
	}
}
