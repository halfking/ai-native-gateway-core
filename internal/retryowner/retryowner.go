// Package retryowner carries the request-scoped retry-ownership decision
// (docs/修订0811/18 §5, docs/修订0811/19 SR-W0).
//
// The gateway currently has several overlapping outer retry owners: the mux
// streamretry wrapper, the Chat Handler Goal loop, the Executor's recursive
// sync/model-fallback retries, and the old async-pending goroutine. When the
// request-survival coordinator is enabled for a request, it must be the ONLY
// outer retry owner for that request's lifetime.
//
// The decision is frozen once per request at the HTTP boundary and carried
// via context. Hot-reloading the survival flag must NOT change the owner of
// an in-flight request — every component reads the frozen value, never the
// global config.
package retryowner

import "context"

// Owner identifies which component owns outer retries for a request.
type Owner string

const (
	// Legacy is the pre-survival ownership graph: streamretry wrapper +
	// Handler Goal loop + Executor recursive retries + async pending,
	// exactly as they behave with request_survival disabled.
	Legacy Owner = "legacy"

	// Survival means the SurvivalCoordinator exclusively owns every retry
	// decision between attempts; all legacy outer loops must pass through
	// (execute at most once) or be skipped for this request.
	Survival Owner = "survival"
)

// ExecutionPath identifies which executor backend serves the request. It is
// snapshotted per request so a hot toggle of dispatch_v2 cannot switch a
// survival request's semantics between attempts.
type ExecutionPath string

const (
	// PathLegacyLoop is the synchronous candidate loop in Executor.Execute.
	PathLegacyLoop ExecutionPath = "legacy_loop"

	// PathDispatchV2 is the multi-tier dispatch pipeline.
	PathDispatchV2 ExecutionPath = "dispatch_v2"
)

type ctxKey struct{ name string }

var (
	ownerKey = ctxKey{"retryowner"}
	pathKey  = ctxKey{"executionpath"}
)

// FreezeOwner returns a context with the retry owner frozen for the request
// lifetime. Re-freezing with a different owner is a programming error and is
// ignored (first freeze wins) — the HTTP boundary is the only legitimate
// freeze point.
func FreezeOwner(ctx context.Context, owner Owner) context.Context {
	if existing, ok := ctx.Value(ownerKey).(Owner); ok && existing != owner {
		// First freeze wins; a disagreeing second freeze means two components
		// believe they own retries. Keep the original decision so behavior
		// stays deterministic; the caller should log loudly upstream.
		return ctx
	}
	return context.WithValue(ctx, ownerKey, owner)
}

// OwnerFrom returns the frozen retry owner, defaulting to Legacy when the
// boundary did not freeze one (all pre-survival callers).
func OwnerFrom(ctx context.Context) Owner {
	if owner, ok := ctx.Value(ownerKey).(Owner); ok && owner != "" {
		return owner
	}
	return Legacy
}

// FreezePath returns a context with the execution path snapshotted for the
// request lifetime. First freeze wins, mirroring FreezeOwner.
func FreezePath(ctx context.Context, path ExecutionPath) context.Context {
	if existing, ok := ctx.Value(pathKey).(ExecutionPath); ok && existing != path {
		return ctx
	}
	return context.WithValue(ctx, pathKey, path)
}

// PathFrom returns the frozen execution path. It returns PathLegacyLoop when
// nothing froze a path yet — call sites must freeze it as soon as the backend
// for the request is known.
func PathFrom(ctx context.Context) ExecutionPath {
	if path, ok := ctx.Value(pathKey).(ExecutionPath); ok && path != "" {
		return path
	}
	return PathLegacyLoop
}
