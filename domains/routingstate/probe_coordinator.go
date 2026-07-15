package routingstate

import (
	"strings"
	"sync"
	"time"
)

type ProbeTrigger string

const (
	ProbeTriggerRequestFailure ProbeTrigger = "request_failure"
	ProbeTriggerNoCandidates   ProbeTrigger = "no_candidates"
	ProbeTriggerScheduled      ProbeTrigger = "scheduled"
	ProbeTriggerRecovery       ProbeTrigger = "recovery"
	ProbeTriggerManual         ProbeTrigger = "manual"
)

type ProbeTask struct {
	CredentialID  int
	RawModelName  string
	Scope         Scope
	Trigger       ProbeTrigger
	Priority      int
	NotBefore     time.Time
	CorrelationID string
}

type ProbeDecision struct {
	Accepted bool
	Reason   string
	Task     ProbeTask
}

type ProbeCoordinator struct {
	mode Mode

	mu      sync.Mutex
	pending map[string]ProbeTask
}

func NewProbeCoordinator(mode Mode) *ProbeCoordinator {
	return &ProbeCoordinator{
		mode:    mode,
		pending: make(map[string]ProbeTask),
	}
}

func (c *ProbeCoordinator) Observe(task ProbeTask) ProbeDecision {
	decision := ProbeDecision{Task: task}
	if c == nil || c.mode != ModeShadow {
		decision.Reason = "disabled"
		return decision
	}
	if task.CredentialID <= 0 {
		decision.Reason = "missing_credential_id"
		return decision
	}
	if task.Scope != ScopeCredential && task.Scope != ScopeModel {
		decision.Reason = "invalid_scope"
		return decision
	}
	if task.Scope == ScopeModel && strings.TrimSpace(task.RawModelName) == "" {
		decision.Reason = "missing_raw_model_name"
		return decision
	}

	key := probeTaskKey(task)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.pending[key]; exists {
		decision.Reason = "duplicate"
		return decision
	}
	c.pending[key] = task
	decision.Accepted = true
	decision.Reason = "shadow_only"
	return decision
}

func (c *ProbeCoordinator) Complete(task ProbeTask) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.pending, probeTaskKey(task))
	c.mu.Unlock()
}

func probeTaskKey(task ProbeTask) string {
	if task.Scope == ScopeCredential {
		return "credential:" + strconvItoa(task.CredentialID)
	}
	return "model:" + strconvItoa(task.CredentialID) + ":" + strings.ToLower(task.RawModelName)
}
