package executors

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credential" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// CommonExecutor holds the protocol-agnostic state and logic for
// executing a request against a credential: retry budget, circuit
// breaker, timeout, credential state writes. It is the substrate
// that protocol-specific executors (chat, anthropic) compose onto.
//
// Phase 1 keeps the surface intentionally small: only the retry loop
// and circuit/limiter handles used by RunWithCredential are wired up.
// The wider field set is declared so future phases (P2 onwards) can
// grow the abstraction without re-plumbing the type.
type CommonExecutor struct {
	Circuit         *credential.Manager
	Limiter         *credential.Limiter
	Pools           *pool.PoolManager
	State           *credential.Writer
	HeaderProfiles  *HeaderProfileCache
	Upstream        *upstream.Client
	FpSlots         *credentialfpslot.Manager
	UpstreamTimeout time.Duration
	StreamTimeout   time.Duration
	// Chunks sent before stream becomes non-resumable (default 50)
	StreamRetryThreshold int

	// Internal: identifies the (provider, credential) pair this
	// CommonExecutor instance is bound to. RunWithCredential uses
	// these to record circuit success/failure. Lower-case because
	// they are only meant to be set by the surrounding Executor /
	// protocol-specific executor at composition time.
	providerID   int
	credentialID int
	// billingMode mirrors the bound candidate's model_offers.billing_mode
	// so RunWithCredential can honor freeCredentialsTolerateTransient.
	// Empty = paid (default), keeps legacy behavior.
	billingMode string
}

// SetProviderCredential binds the CommonExecutor instance to a
// specific (providerID, credentialID) pair so RunWithCredential can
// record circuit success/failure. Called by the surrounding Executor
// before invoking a protocol-specific executor.
func (c *CommonExecutor) SetProviderCredential(providerID, credentialID int, billingMode string) {
	c.providerID = providerID
	c.credentialID = credentialID
	c.billingMode = billingMode
}
