package executors

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credential" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// CommonExecutor holds the protocol-agnostic state for executing a
// request against a credential: circuit breaker, limiter, pools,
// timeout, credential state handles. It is the substrate that
// protocol-specific executors (chat, anthropic) compose onto.
//
// The Phase-1 RunWithCredential retry loop was retired with the legacy
// sync candidate loop (AUDIT_24H B2b, 2026-08-17) — it had no
// production caller. The wider field set is declared so future phases
// can grow the abstraction without re-plumbing the type.
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
	// CommonExecutor instance is bound to (see SetProviderCredential).
	// Lower-case because they are only meant to be set by the
	// surrounding Executor / protocol-specific executor at composition
	// time.
	providerID   int
	credentialID int
	// billingMode mirrors the bound candidate's model_offers.billing_mode
	// for future policy hooks (free-credential tolerance semantics).
	// Empty = paid (default), keeps legacy behavior.
	billingMode string
}

// SetProviderCredential binds the CommonExecutor instance to a
// specific (providerID, credentialID) pair. Called by the surrounding
// Executor before invoking a protocol-specific executor.
func (c *CommonExecutor) SetProviderCredential(providerID, credentialID int, billingMode string) {
	c.providerID = providerID
	c.credentialID = credentialID
	c.billingMode = billingMode
}
