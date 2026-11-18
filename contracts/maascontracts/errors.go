package maascontracts

// Error represents a structured error response from MaaS operations per OpenAPI v0.1.
// It includes a machine-readable error code, human-readable message,
// retry guidance, and optional trace information for debugging.
//
// The error response follows RFC 7807 (application/problem+json) conventions.
type Error struct {
	Code      string  `json:"code"`               // Machine-readable error code (see const definitions below)
	Message   string  `json:"message"`            // Human-readable error description
	Retryable bool    `json:"retryable"`          // Whether the client should retry with same idempotency key
	TraceID   *string `json:"trace_id,omitempty"` // Optional distributed trace ID for debugging
}

// Error implements the error interface.
func (e *Error) Error() string {
	return "maascontracts: " + e.Code + ": " + e.Message
}

// Error code constants defined by OpenAPI contract v0.1.
// These codes must be stable across versions for client compatibility.
const (
	// Idempotency error codes
	ErrorIdempotencyReplay          = "MAAS_IDEMPOTENCY_REPLAY"           // Same key, same payload, returning cached result
	ErrorIdempotencyPayloadConflict = "MAAS_IDEMPOTENCY_PAYLOAD_CONFLICT" // Same key, different payload (HTTP 409)

	// Concurrency control error codes
	ErrorConflictRevision = "MAAS_CONFLICT_REVISION" // Optimistic concurrency check failed (expected_version mismatch)

	// Inventory domain error codes
	ErrorSellableInsufficient = "MAAS_SELLABLE_INSUFFICIENT" // Not enough inventory available
	ErrorHoldExpired          = "MAAS_HOLD_EXPIRED"          // Hold has expired, cannot confirm
	ErrorHoldFenceMismatch    = "MAAS_HOLD_FENCE_MISMATCH"   // Fencing token does not match
	ErrorReserveNotCommitted  = "MAAS_RESERVE_NOT_COMMITTED" // Hold not in committed state
	ErrorAlreadySold          = "MAAS_ALREADY_SOLD"          // Resource already consumed
	ErrorStaleObservation     = "MAAS_STALE_OBSERVATION"     // Resource snapshot is stale
	ErrorReconfirmRequired    = "MAAS_RECONFIRM_REQUIRED"    // Price/inventory changed, must reconfirm

	// Entitlement domain error codes
	ErrorLegalNotBinding = "MAAS_LEGAL_NOT_BINDING" // Required legal_signoff_ref is missing or invalid

	// Authorization error codes
	ErrorUnauthorizedScope = "MAAS_UNAUTHORIZED_SCOPE" // Insufficient permission scopes

	// Ledger domain error codes
	ErrorUsageAlreadySettled      = "MAAS_USAGE_ALREADY_SETTLED"      // Usage hold already settled, cannot modify
	ErrorDoubleSettlementRejected = "MAAS_DOUBLE_SETTLEMENT_REJECTED" // Attempted to settle twice (forbidden)

	// Generic retry guidance
	ErrorRetryableUnknown = "MAAS_RETRYABLE_UNKNOWN" // Transient error, client may retry with same idempotency key
)
