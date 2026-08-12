package dispatch

import (
	"context"
	"sync/atomic"
	"time"
)

// Concurrency modes (mirror credentials.concurrency_mode CHECK, migration 479).
const (
	ModeConcurrency = "concurrency" // in-flight hard cap (DeepSeek/智谱/Kimi)
	ModeRPM         = "rpm"         // requests-per-minute token bucket (OpenAI/Gemini/Groq)
	ModeTPM         = "tpm"         // tokens-per-minute token bucket (OpenAI/Anthropic/Azure)
	ModeDisabled    = "disabled"    // no limiting
)

// CredentialRef is dispatch's minimal, decoupled view of a credential. The
// executor adapts provider.Candidate → CredentialRef. dispatch never imports
// the provider package (avoids the executors↔dispatch cycle).
type CredentialRef struct {
	CredentialID     int
	ProviderID       int
	ConcurrencyMode  string
	ConcurrencyLimit int    // in-flight cap (concurrency mode); 0 = unlimited
	RPMLimit         int    // req/min (rpm mode); 0 = unlimited
	TPMLimit         int    // tokens/min (tpm mode); 0 = unlimited
	MaxQueueDepth    int    // 0 = use global Config.MaxQueueDepth
	MaxQueueWaitMS   int    // 0 = use global Config.MaxQueueWaitMS
	Vendor           string // 原厂/供应商, for stats labels
}

// ForwardOutcome is what ForwardFunc returns.
type ForwardOutcome struct {
	// Result is the opaque success payload (e.g. *executors.ExecuteResult).
	// dispatch never inspects it; it is forwarded to the Submit caller.
	Result any
	// BytesSent is true if any byte reached the client (first-byte
	// boundary). When true, a subsequent error MUST NOT trigger a
	// cross-credential switch — the request is completed with the error
	// (ADR-Disp-003).
	BytesSent bool
	Err       error
}

// QueuedRequest is the unit of work flowing through the pipeline.
//
// INVARIANT (single-owner): at any instant exactly one executor goroutine
// owns a *QueuedRequest (hand-off, never shared). The mutable failover
// fields below are therefore safe to mutate WITHOUT a mutex — the single
// current owner is the only writer.
type QueuedRequest struct {
	ID             string // request_id
	TenantID       string
	RequestedModel string // client model (may be "auto")
	ResolvedModel  string // set by the dispatcher after auto-resolution

	// Ctx is the request context. Used by governors/forwarders for pacing
	// waits and to observe client disconnect.
	Ctx context.Context

	// Payload is opaque to dispatch. RouteFunc/ForwardFunc interpret it
	// (e.g. *executors.ExecParams).
	Payload any

	// Request-level failover policy. The Pipeline-wide switches are kill
	// switches; these fields are the per-request authority derived by the caller
	// from the original client model and routing constraints.
	AllowModelChange    bool
	AllowProviderChange bool
	// RetryPerCredential overrides Config.RetryPerCredential when >= 0. This
	// keeps policy.RetryPerCredential=0 meaningful for requests that must
	// switch immediately after a pre-firstbyte failure.
	RetryPerCredential int
	ModelAlternatives  []string
	InitialProviderID  int

	// EstimatedTokens is the pre-send token estimate used by the tpm
	// governor. 0 = unknown (the tpm governor uses a conservative fixed cost).
	EstimatedTokens int

	// SelectedCred is the credential the dispatcher/mover chose for the
	// current attempt. Set before enqueueing into a Tier-2 credential queue.
	SelectedCred CredentialRef

	// ResultCh signals completion to the Submit caller. Capacity 1; sent on
	// exactly once (guarded by the completed atomic).
	ResultCh chan ForwardOutcome

	// Failover tracking — single-owner mutation.
	TriedCredentials map[int]struct{}
	TriedModels      map[string]struct{}
	CredRetryCount   int
	// AttemptCount is the total number of forward attempts across all
	// credentials. Bounded by maxAttempts to prevent a request from looping
	// through an unbounded candidate set under pathological conditions.
	AttemptCount int

	EnqueuedAt    time.Time // Tier-1 enqueue time (for queue-wait metrics)
	CredEnqueuedAt time.Time // Tier-2 (credential queue) enqueue time
	DequeuedAt    time.Time // set when leaving Tier-2 (governor acquire)

	// completed guarantees ResultCh is sent on exactly once even if a bug
	// would otherwise double-complete.
	completed atomic.Bool

	// abandoned is set by Submit when the caller's ctx expired before the
	// pipeline finished. complete() then drops the result (no reader left).
	abandoned atomic.Bool

	// vendor snapshot for stats labels (from SelectedCred at enqueue time).
	vendor string
}

// NewQueuedRequest constructs a QueuedRequest ready for Pipeline.Submit.
func NewQueuedRequest(id, tenantID, model string, ctx context.Context, payload any) *QueuedRequest {
	return &QueuedRequest{
		ID:                 id,
		TenantID:           tenantID,
		RequestedModel:     model,
		Ctx:                ctx,
		Payload:            payload,
		ResultCh:           make(chan ForwardOutcome, 1),
		TriedCredentials:   make(map[int]struct{}),
		TriedModels:        make(map[string]struct{}),
		RetryPerCredential: -1,
		EnqueuedAt:         time.Now(),
	}
}

// markTriedCredential records a credential as exhausted for this request.
func (qr *QueuedRequest) markTriedCredential(id int) {
	qr.TriedCredentials[id] = struct{}{}
}

// markTriedModel records a model as exhausted for this request.
func (qr *QueuedRequest) markTriedModel(model string) {
	if model == "" {
		return
	}
	qr.TriedModels[model] = struct{}{}
}

// HasTriedCredential reports whether the credential was already exhausted.
// Exported so the executor-supplied RouteFunc can filter tried credentials.
func (qr *QueuedRequest) HasTriedCredential(id int) bool {
	_, ok := qr.TriedCredentials[id]
	return ok
}

// hasTriedCredential is the internal alias.
func (qr *QueuedRequest) hasTriedCredential(id int) bool { return qr.HasTriedCredential(id) }
