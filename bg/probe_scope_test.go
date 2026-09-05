package bg

import (
	"context"
	"errors"
	"testing"
)

type probeScopeStub struct{ allowed bool }

func (s probeScopeStub) AllowsIdentity(string, int, string) bool { return s.allowed }

func TestProbeServiceRejectsOutOfScopeBeforeProbeRounds(t *testing.T) {
	called := false
	service := newTestProbeService(
		nodeProbeRoundResult{ok: true},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true},
		func(context.Context, probeOutcome) { called = true },
	)
	service.SetScope(probeScopeStub{allowed: false})
	result, err := service.Run(context.Background(), ProbeQueueTask{
		CredentialID: 42, TenantID: "other", RawModel: "nodehealth-test-model", Attempt: 1,
	})
	if !errors.Is(err, ErrProbeOutOfScope) {
		t.Fatalf("Run error=%v, want ErrProbeOutOfScope", err)
	}
	if result.Status != ProbeQueueSuccess || result.ReasonCode != "probe_out_of_scope" {
		t.Fatalf("result=%+v", result)
	}
	if called {
		t.Fatal("out-of-scope task applied probe outcome")
	}
}
