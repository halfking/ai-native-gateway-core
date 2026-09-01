package errorsx

import "time"

// ActionPhase identifies the request lifecycle phase in which an error was
// observed. It is intentionally independent from any transport package so the
// same policy can be used by dispatch, streaming, and durable workers.
type ActionPhase string

const (
	PhaseUpstream   ActionPhase = "upstream"
	PhaseStreaming  ActionPhase = "streaming"
	PhaseSettlement ActionPhase = "settlement"
)

// ActionCommitState is the policy-layer projection of a stream commit state.
// Metadata can be discarded safely; content/tool/terminal output is already
// client-visible and cannot be transparently replayed.
type ActionCommitState int

const (
	ActionCommitNone ActionCommitState = iota
	ActionCommitMetadata
	ActionCommitContent
	ActionCommitToolCall
	ActionCommitTerminal
)

// NextAction is the side-effect-free instruction produced by DecideNextAction.
// Callers execute the instruction; this package never sleeps, switches nodes,
// writes a response, or mutates provider state.
type NextAction string

const (
	ActionRetrySameNode  NextAction = "retry_same_node"
	ActionSwitchNode     NextAction = "switch_node"
	ActionWaitRecovery   NextAction = "wait_recovery"
	ActionResumeBlocked  NextAction = "resume_blocked"
	ActionFailTerminal   NextAction = "fail_terminal"
	ActionClientCanceled NextAction = "client_canceled"
	ActionFailClosed     NextAction = "fail_closed"
)

func (a NextAction) String() string { return string(a) }

// DecisionContext carries the request, route, workflow, and client state needed
// to turn an ErrorKind into one next action. IDs are copied into ActionDecision
// for logs and observations, so consumers do not need to rebuild correlation.
// ActionNode identifies a provider/credential/model combination without any
// credential material. It is the stable fingerprint used to prevent a request
// from cycling back into an exhausted route.
type ActionNode struct {
	Model        string
	ProviderID   int
	CredentialID int
}

// PriorAttempt is a bounded, content-free projection of one earlier attempt or
// routing decision. Callers may build it from dispatch journal/attempt records;
// the policy never mutates or appends to the supplied history.
type PriorAttempt struct {
	Seq                int
	AttemptNo          int
	Model              string
	ProviderID         int
	CredentialID       int
	Kind               ErrorKind
	Action             NextAction
	RetryAfter         time.Duration
	Committed          bool
	ClientDisconnected bool
}

// DecisionHistory is the request-local history needed for loop prevention.
// Entries should be oldest-to-newest and bounded by the caller (the dispatch
// journal currently uses a 128-entry ring). TriedNodes is an explicit snapshot
// of the caller's monotonic exclusion set and is not inferred from counts.
type DecisionHistory struct {
	PriorAttempts []PriorAttempt
	TriedNodes    []ActionNode
	TriedModels   []string
	Terminal      bool
	LastSeq       int
}

// Clone returns a detached history snapshot suitable for passing across a
// retry boundary. The policy never mutates caller-owned slices.
func (h DecisionHistory) Clone() DecisionHistory {
	out := h
	out.PriorAttempts = append([]PriorAttempt(nil), h.PriorAttempts...)
	out.TriedNodes = append([]ActionNode(nil), h.TriedNodes...)
	out.TriedModels = append([]string(nil), h.TriedModels...)
	return out
}

// DecisionContext carries the request, route, workflow, and client state needed
// to turn an ErrorKind into one next action. IDs are copied into ActionDecision
// for logs and observations, so consumers do not need to rebuild correlation.
type DecisionContext struct {
	RequestID      string
	TenantID       string
	SessionID      string
	ClientProtocol string

	Phase              ActionPhase
	AttemptNo          int
	SameNodeRetryCount int
	MaxSameNodeRetries int
	RemainingAttempts  int

	ProviderID          int
	CredentialID        int
	ResolvedModel       string
	HasAlternateNode    bool
	AllowProviderChange bool
	AllowModelChange    bool
	History             DecisionHistory

	CommitState           ActionCommitState
	SemanticOutputVisible bool
	ClientDisconnected    bool

	Kind       ErrorKind
	HTTPStatus int
	RetryAfter time.Duration
}

// ActionDecision is a pure policy result. The side-effect flags are intents for
// the owning layer and are deliberately not coupled to breaker/probe APIs.
type ActionDecision struct {
	Action                  NextAction
	Kind                    ErrorKind
	ReasonCode              string
	HistorySeq              int
	LoopDetected            bool
	AlreadyAttempted        bool
	ExhaustedSameNodeBudget bool
	ExhaustedCandidateSpace bool
	AvoidCurrentNode        bool
	RetryAfter              time.Duration

	PreserveClientConnection  bool
	RequiresUncommittedOutput bool
	RecordProviderFailure     bool
	RecordCredentialFailure   bool
	RecordCredentialState     bool
	EnqueueProbe              bool

	RequestID      string
	TenantID       string
	SessionID      string
	ClientProtocol string
	Phase          ActionPhase
	AttemptNo      int
	ProviderID     int
	CredentialID   int
	ResolvedModel  string
}

func (d ActionDecision) withContext(ctx DecisionContext) ActionDecision {
	d.RequestID = ctx.RequestID
	d.TenantID = ctx.TenantID
	d.SessionID = ctx.SessionID
	d.ClientProtocol = ctx.ClientProtocol
	d.Phase = ctx.Phase
	d.AttemptNo = ctx.AttemptNo
	d.ProviderID = ctx.ProviderID
	d.CredentialID = ctx.CredentialID
	d.ResolvedModel = ctx.ResolvedModel
	d.Kind = ctx.Kind
	return d
}

func validateDecisionHistory(history DecisionHistory) string {
	if history.LastSeq < 0 {
		return "invalid_history_seq"
	}
	previous := 0
	for _, entry := range history.PriorAttempts {
		if entry.Seq > 0 && entry.Seq <= previous {
			return "history_seq_not_monotonic"
		}
		if entry.Seq > 0 {
			previous = entry.Seq
		}
	}
	for _, node := range history.TriedNodes {
		if node.Model == "" || node.CredentialID <= 0 {
			return "invalid_history_node"
		}
	}
	return ""
}

func historyContainsTriedNode(history DecisionHistory, node ActionNode) bool {
	for _, tried := range history.TriedNodes {
		if tried == node {
			return true
		}
	}
	return false
}

func historyContainsNode(history DecisionHistory, node ActionNode) bool {
	if node.Model == "" || node.CredentialID <= 0 {
		return false
	}
	for _, tried := range history.TriedNodes {
		if tried == node {
			return true
		}
	}
	for _, prior := range history.PriorAttempts {
		if prior.Model == node.Model && prior.ProviderID == node.ProviderID && prior.CredentialID == node.CredentialID {
			return true
		}
	}
	return false
}

func historyNodeRetryCount(history DecisionHistory, node ActionNode) int {
	count := 0
	for _, prior := range history.PriorAttempts {
		if prior.Model == node.Model && prior.ProviderID == node.ProviderID && prior.CredentialID == node.CredentialID && prior.Action == ActionRetrySameNode {
			count++
		}
	}
	return count
}

func retryableActionKind(kind ErrorKind) bool {
	switch kind {
	case KindEmptyResponse, KindTransient, KindTimeout, KindNetwork, KindStreamTimeout, KindConcurrent,
		KindUpstreamOverloaded, KindUpstreamDown:
		return true
	default:
		return false
	}
}

func terminalActionKind(kind ErrorKind) bool {
	switch kind {
	case KindAuthRevoked, KindQuotaPermanent, KindModelNotFound,
		KindModelDeprecated, KindContextLength, KindUnsupportedFeature,
		KindContentFilter, KindConversion, KindUpstreamContextLoss,
		KindToolCallIdMismatch, KindClientBug,
		// 2026-09-01 (P1-1 24h-audit round2): KindCircuitOpen /
		// KindFpSlotSaturated are pure gateway-side admission signals
		// written by dispatch preflight rejections (executor_dispatch.go
		// logDispatchPreflightRejection). No upstream call was made, so an
		// upstream retry (same node or sibling) must not be triggered from
		// this policy layer — the dispatch governor already re-plans the
		// next candidate when the breaker/slot rejects one. Mapping them
		// terminal (fail-terminal with the kind as reason) keeps them out
		// of the unmapped-kind fail-closed default.
		KindCircuitOpen, KindFpSlotSaturated:
		return true
	default:
		return false
	}
}

// DecideNextAction is the single request-level interpretation of an error.
// Empty responses and transport interruptions are retried on the same node
// only while output remains uncommitted and the node-local budget remains.
// Once that budget is exhausted, the caller may switch to a sibling node.
func DecideNextAction(ctx DecisionContext) ActionDecision {
	base := ActionDecision{}.withContext(ctx)
	base.HistorySeq = ctx.History.LastSeq

	if err := validateDecisionHistory(ctx.History); err != "" {
		base.Action = ActionFailClosed
		base.ReasonCode = err
		base.LoopDetected = true
		return base
	}
	if ctx.History.Terminal {
		base.Action = ActionFailClosed
		base.ReasonCode = "history_terminal"
		base.LoopDetected = true
		return base
	}
	current := ActionNode{Model: ctx.ResolvedModel, ProviderID: ctx.ProviderID, CredentialID: ctx.CredentialID}
	base.AlreadyAttempted = historyContainsNode(ctx.History, current)
	// A prior attempt is normal for a bounded same-node retry. Only an
	// explicit tried/exhausted node marks a loop: dispatch owns that monotonic
	// exclusion set and clears it only when the model changes.
	currentTried := historyContainsTriedNode(ctx.History, current)
	if currentTried && ctx.Kind != KindCanceled && ctx.CommitState < ActionCommitContent && !ctx.SemanticOutputVisible {
		base.LoopDetected = true
		base.AvoidCurrentNode = true
	}
	effectiveRetryCount := ctx.SameNodeRetryCount
	if historyRetries := historyNodeRetryCount(ctx.History, current); historyRetries > effectiveRetryCount {
		effectiveRetryCount = historyRetries
	}

	if ctx.ClientDisconnected || ctx.Kind == KindCanceled {
		base.Action = ActionClientCanceled
		base.ReasonCode = "client_canceled"
		return base
	}

	if ctx.Kind == "" {
		base.Action = ActionFailClosed
		base.ReasonCode = "missing_error_kind"
		return base
	}

	if ctx.CommitState >= ActionCommitContent || ctx.SemanticOutputVisible {
		if retryableActionKind(ctx.Kind) || ctx.Kind == KindRateLimit || ctx.Kind == KindConcurrent || ctx.Kind == KindUpstreamDown || ctx.Kind == KindUpstreamOverloaded {
			base.Action = ActionResumeBlocked
			base.ReasonCode = "committed_output"
			base.RequiresUncommittedOutput = true
			base.RecordProviderFailure = true
			return base
		}
	}

	if terminalActionKind(ctx.Kind) {
		base.Action = ActionFailTerminal
		base.ReasonCode = string(ctx.Kind)
		return base
	}

	if retryableActionKind(ctx.Kind) {
		base.PreserveClientConnection = true
		base.RequiresUncommittedOutput = true
		base.RecordProviderFailure = true
		base.EnqueueProbe = true
		if !base.LoopDetected && effectiveRetryCount < ctx.MaxSameNodeRetries && ctx.RemainingAttempts > 0 {
			base.Action = ActionRetrySameNode
			base.ReasonCode = "precommit_transient_retry"
			if ctx.RetryAfter > 0 {
				base.RetryAfter = ctx.RetryAfter
			}
			return base
		}
		if ctx.HasAlternateNode && ctx.RemainingAttempts > 0 {
			base.Action = ActionSwitchNode
			base.ReasonCode = "node_retry_budget_exhausted"
			return base
		}
		base.Action = ActionFailTerminal
		base.ReasonCode = "recovery_exhausted"
		return base
	}

	switch ctx.Kind {
	case KindAuth:
		base.Action = ActionSwitchNode
		base.ReasonCode = "credential_auth_failed"
		base.RecordCredentialFailure = true
		base.RecordCredentialState = true
	case KindQuota, KindQuotaPeriodic, KindQuotaBalance:
		// Quota recovery is a wait-state at task level. Dispatch may still
		// switch away from the fatal credential before reaching this policy;
		// this result keeps durable/survival workers from hot-looping while the
		// quota window or balance state recovers.
		base.Action = ActionWaitRecovery
		base.ReasonCode = "credential_quota_exhausted"
		base.RecordCredentialFailure = true
		base.RecordCredentialState = true
		base.RetryAfter = ctx.RetryAfter
		if base.RetryAfter <= 0 {
			base.RetryAfter = defaultRecoveryDelay(ctx.Kind)
		}
	case KindRateLimit, KindUpstreamDown, KindNoAvailableChannel:
		base.Action = ActionWaitRecovery
		base.ReasonCode = string(ctx.Kind)
		base.RecordProviderFailure = true
		base.EnqueueProbe = true
		base.PreserveClientConnection = true
		base.RetryAfter = ctx.RetryAfter
		if base.RetryAfter <= 0 {
			base.RetryAfter = defaultRecoveryDelay(ctx.Kind)
		}
	default:
		base.Action = ActionFailClosed
		base.ReasonCode = "unmapped_kind:" + string(ctx.Kind)
	}

	if base.Action == ActionSwitchNode && !ctx.HasAlternateNode {
		base.Action = ActionFailTerminal
		base.ReasonCode = "no_alternate_node:" + base.ReasonCode
	}
	return base
}

func defaultRecoveryDelay(kind ErrorKind) time.Duration {
	switch kind {
	case KindRateLimit:
		return 30 * time.Second
	case KindConcurrent, KindUpstreamOverloaded:
		return 5 * time.Second
	case KindNoAvailableChannel:
		return 15 * time.Minute
	default:
		return 2 * time.Second
	}
}
