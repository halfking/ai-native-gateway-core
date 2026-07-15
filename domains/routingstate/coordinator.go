package routingstate

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	ModeOff           Mode = "off"
	ModeShadow        Mode = "shadow"
	ModeAuthoritative Mode = "authoritative"
)

type Scope string

const (
	ScopeCredential Scope = "credential"
	ScopeModel      Scope = "model"
)

type Source string

const (
	SourceRequest         Source = "request"
	SourceChecker         Source = "checker"
	SourceActiveProbe     Source = "active_probe"
	SourceNodeProbe       Source = "node_probe"
	SourceModelProbe      Source = "model_probe"
	SourceCredentialProbe Source = "credential_probe"
	SourcePassiveProbe    Source = "passive_probe"
	SourceRecovery        Source = "recovery"
	SourceAdmin           Source = "admin"
)

type Evidence struct {
	CredentialID      int
	RawModelName      string
	CanonicalName     string
	Scope             Scope
	Source            Source
	ObservedAt        time.Time
	Generation        uint64
	CorrelationID     string
	AvailabilityState string
	HealthStatus      string
	BindingAvailable  *bool
	ErrorKind         string
}

type Transition struct {
	Accepted  bool
	Applied   bool
	Reason    string
	Evidence  Evidence
	CreatedAt time.Time
}

type Coordinator struct {
	mode Mode

	mu   sync.Mutex
	last map[string]Evidence
}

func NewCoordinator(mode Mode) *Coordinator {
	return &Coordinator{
		mode: mode,
		last: make(map[string]Evidence),
	}
}

func (c *Coordinator) Observe(evidence Evidence) Transition {
	transition := Transition{Evidence: evidence, CreatedAt: time.Now()}
	if c == nil || c.mode != ModeShadow {
		transition.Reason = "disabled"
		return transition
	}
	if reason := validateEvidence(evidence); reason != "" {
		transition.Reason = reason
		return transition
	}

	key := evidenceKey(evidence)
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.last[key]; ok && isStale(evidence, previous) {
		transition.Reason = "stale_evidence"
		return transition
	}
	c.last[key] = evidence
	transition.Accepted = true
	transition.Reason = "shadow_only"
	return transition
}

func validateEvidence(evidence Evidence) string {
	if evidence.CredentialID <= 0 {
		return "missing_credential_id"
	}
	if evidence.Scope != ScopeCredential && evidence.Scope != ScopeModel {
		return "invalid_scope"
	}
	if evidence.Scope == ScopeModel && strings.TrimSpace(evidence.RawModelName) == "" {
		return "missing_raw_model_name"
	}
	if strings.TrimSpace(evidence.CanonicalName) == "" {
		return "missing_canonical_name"
	}
	if evidence.ObservedAt.IsZero() {
		return "missing_observed_at"
	}
	return ""
}

func evidenceKey(evidence Evidence) string {
	if evidence.Scope == ScopeCredential {
		return "credential:" + strconvItoa(evidence.CredentialID)
	}
	return "model:" + strconvItoa(evidence.CredentialID) + ":" + strings.ToLower(evidence.RawModelName)
}

func isStale(current, previous Evidence) bool {
	if current.Generation > 0 || previous.Generation > 0 {
		return current.Generation <= previous.Generation
	}
	return !current.ObservedAt.After(previous.ObservedAt)
}

func strconvItoa(value int) string {
	return strconv.Itoa(value)
}
