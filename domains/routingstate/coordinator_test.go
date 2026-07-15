package routingstate

import (
	"testing"
	"time"
)

func TestCoordinatorShadowAcceptsFreshModelEvidence(t *testing.T) {
	coordinator := NewCoordinator(ModeShadow)
	observedAt := time.Now()
	transition := coordinator.Observe(Evidence{
		CredentialID:  7,
		RawModelName:  "MiniMax-M3",
		CanonicalName: "minimax-m3",
		Scope:         ScopeModel,
		Source:        SourceNodeProbe,
		ObservedAt:    observedAt,
	})

	if !transition.Accepted || transition.Applied || transition.Reason != "shadow_only" {
		t.Fatalf("unexpected transition: %+v", transition)
	}
}

func TestCoordinatorRejectsNonShadowModes(t *testing.T) {
	evidence := Evidence{
		CredentialID:  7,
		RawModelName:  "minimax-m3",
		CanonicalName: "minimax-m3",
		Scope:         ScopeModel,
		Source:        SourceRequest,
		ObservedAt:    time.Now(),
	}
	for _, mode := range []Mode{ModeOff, ModeAuthoritative} {
		transition := NewCoordinator(mode).Observe(evidence)
		if transition.Accepted || transition.Applied || transition.Reason != "disabled" {
			t.Fatalf("mode %q transition: %+v", mode, transition)
		}
	}
}

func TestCoordinatorRejectsStaleEvidence(t *testing.T) {
	coordinator := NewCoordinator(ModeShadow)
	observedAt := time.Now()
	first := Evidence{
		CredentialID:  7,
		RawModelName:  "minimax-m3",
		CanonicalName: "minimax-m3",
		Scope:         ScopeModel,
		Source:        SourceNodeProbe,
		ObservedAt:    observedAt,
	}
	if transition := coordinator.Observe(first); !transition.Accepted {
		t.Fatalf("first evidence rejected: %+v", transition)
	}

	stale := first
	stale.ObservedAt = observedAt.Add(-time.Second)
	if transition := coordinator.Observe(stale); transition.Accepted || transition.Reason != "stale_evidence" {
		t.Fatalf("stale evidence result: %+v", transition)
	}
}

func TestCoordinatorRequiresRawModelForModelScope(t *testing.T) {
	transition := NewCoordinator(ModeShadow).Observe(Evidence{
		CredentialID:  7,
		CanonicalName: "minimax-m3",
		Scope:         ScopeModel,
		Source:        SourceRequest,
		ObservedAt:    time.Now(),
	})
	if transition.Reason != "missing_raw_model_name" {
		t.Fatalf("reason = %q, want missing_raw_model_name", transition.Reason)
	}
}

func TestProbeCoordinatorReleasesShadowTask(t *testing.T) {
	coordinator := NewProbeCoordinator(ModeShadow)
	task := ProbeTask{
		CredentialID: 7,
		RawModelName: "minimax-m3",
		Scope:        ScopeModel,
		Trigger:      ProbeTriggerRequestFailure,
	}
	if decision := coordinator.Observe(task); !decision.Accepted {
		t.Fatalf("first decision: %+v", decision)
	}
	coordinator.Complete(task)
	if decision := coordinator.Observe(task); !decision.Accepted {
		t.Fatalf("released task should be observed again: %+v", decision)
	}
}

func TestProbeCoordinatorDeduplicatesByScope(t *testing.T) {
	coordinator := NewProbeCoordinator(ModeShadow)
	modelTask := ProbeTask{
		CredentialID: 7,
		RawModelName: "MiniMax-M3",
		Scope:        ScopeModel,
		Trigger:      ProbeTriggerRequestFailure,
	}
	if decision := coordinator.Observe(modelTask); !decision.Accepted {
		t.Fatalf("model task rejected: %+v", decision)
	}

	duplicate := modelTask
	duplicate.RawModelName = "minimax-m3"
	if decision := coordinator.Observe(duplicate); decision.Accepted || decision.Reason != "duplicate" {
		t.Fatalf("duplicate decision: %+v", decision)
	}

	credentialTask := ProbeTask{
		CredentialID: 7,
		Scope:        ScopeCredential,
		Trigger:      ProbeTriggerScheduled,
	}
	if decision := coordinator.Observe(credentialTask); !decision.Accepted {
		t.Fatalf("credential task rejected: %+v", decision)
	}
}
