package routingstate

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	stateCoordinatorModeKey = "routing_state.coordinator_mode"
	probeCoordinatorModeKey = "routing_state.probe_coordinator_mode"
)

// ShadowObserver is the optional executor seam for passive state/probe
// observation. It never writes routing state, invalidates caches, or dispatches
// probes. Platform settings are read for every event so shadow mode is hot
// reloadable without rebuilding the gateway.
type ShadowObserver struct {
	state *Coordinator
	probe *ProbeCoordinator
}

func NewShadowObserver() *ShadowObserver {
	return &ShadowObserver{
		state: NewCoordinator(ModeShadow),
		probe: NewProbeCoordinator(ModeShadow),
	}
}

func (o *ShadowObserver) ObserveState(evidence Evidence) {
	if o == nil || !shadowEnabled(stateCoordinatorModeKey) {
		return
	}
	transition := o.state.Observe(evidence)
	recordEvidence(evidence.Source, evidence.Scope, transition.Accepted, transition.Reason)
}

func (o *ShadowObserver) ObserveProbe(task ProbeTask) {
	if o == nil || !shadowEnabled(probeCoordinatorModeKey) {
		return
	}
	decision := o.probe.Observe(task)
	recordProbeDecision(task.Trigger, task.Scope, decision.Accepted, decision.Reason)
	if decision.Accepted {
		// Shadow mode owns no execution lifecycle. Release immediately so a
		// previous observation never suppresses a later real probe signal.
		o.probe.Complete(task)
	}
}

func shadowEnabled(key string) bool {
	return strings.EqualFold(strings.TrimSpace(settings.GetPlatformString(key, "off")), string(ModeShadow))
}
