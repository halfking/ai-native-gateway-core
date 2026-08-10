package credential

import (
	"sync"
	"time"

	"log/slog"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// KeyStatus is the per-key health state tracked by KeyRotator.
type KeyStatus string

const (
	KeyStatusActive   KeyStatus = "active"   // healthy, eligible for rotation
	KeyStatusWarning  KeyStatus = "warning"  // recent failure, still eligible
	KeyStatusInvalid  KeyStatus = "invalid"  // exhausted/banned, skipped until reset
	KeyStatusTerminal KeyStatus = "terminal" // permanently dead (402 balance), never retried
)

// failureThreshold is the consecutive-failure count after which a key is marked
// invalid (mirrors OmniRoute apiKeyRotator.ts FAILURE_THRESHOLD = 2).
const failureThreshold = 2

// keyHealth is the in-memory per-key health record. Not persisted; a process
// restart resets all keys to active (intentional, matches OmniRoute behavior).
type keyHealth struct {
	status              KeyStatus
	consecutiveFailures int
	totalRequests       int64
	totalFailures       int64
	lastSuccess         time.Time
	lastFailure         time.Time
}

// KeyRotator holds per-credential multi-key rotation state. It is the Go
// analogue of OmniRoute's apiKeyRotator.ts: round-robin across a credential's
// keys, skipping invalid/terminal keys, with per-key failure tracking so one
// depleted key doesn't kill the whole credential.
//
// Thread-safe. Lives in-memory; the credential_keys table persists only the
// encrypted key material and terminal status (for cross-process sharing of
// 402-balance-exhausted keys).
type KeyRotator struct {
	mu sync.Mutex
	// states keyed by credentialID → slice indexed by key index (0 = primary,
	// 1..N = extras from credential_keys table).
	states map[int][]keyHealth
	// round-robin cursors per credential.
	cursors map[int]int
}

// NewKeyRotator creates an empty rotator.
func NewKeyRotator() *KeyRotator {
	return &KeyRotator{
		states:  make(map[int][]keyHealth),
		cursors: make(map[int]int),
	}
}

// EnsureCred registers a credential's key count with the rotator. Keys are
// numbered 0..count-1 (0 is the primary in credentials.secret_ciphertext, the
// rest are extras from credential_keys). Safe to call repeatedly; only grows
// the slice when the count increases (e.g. after a key was added).
func (kr *KeyRotator) EnsureCred(credentialID, count int) {
	if count <= 1 {
		return
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	existing := kr.states[credentialID]
	if len(existing) >= count {
		return
	}
	// grow, preserving prior health
	grown := make([]keyHealth, count)
	copy(grown, existing)
	for i := len(existing); i < count; i++ {
		grown[i] = keyHealth{status: KeyStatusActive}
	}
	kr.states[credentialID] = grown
}

// ResolveKey picks the next eligible key index for a credential via round-robin,
// skipping invalid/terminal keys. Returns the key index to use.
//
// stickyIdx (if >= 0 and still healthy) is reused — this is the multi-turn
// stream pinning analogue of OmniRoute's resolveKeyForRequest, preserving
// prompt-cache affinity across turns. When stickyIdx is invalid or the key is
// exhausted, falls back to round-robin.
//
// Returns -1 when ALL keys are invalid/terminal (caller should propagate the
// failure to the credential-level breaker, since the credential as a whole is
// unusable).
func (kr *KeyRotator) ResolveKey(credentialID, stickyIdx int) int {
	kr.mu.Lock()
	defer kr.mu.Unlock()

	states, ok := kr.states[credentialID]
	if !ok || len(states) == 0 {
		return 0 // single-key credential (or unregistered): always primary
	}

	// sticky reuse
	if stickyIdx >= 0 && stickyIdx < len(states) && isEligible(states[stickyIdx].status) {
		return stickyIdx
	}

	n := len(states)
	// round-robin from the cursor, scanning all positions once
	start := kr.cursors[credentialID]
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		if isEligible(states[idx].status) {
			kr.cursors[credentialID] = (idx + 1) % n
			return idx
		}
	}
	return -1 // all keys exhausted
}

func isEligible(s KeyStatus) bool {
	return s == KeyStatusActive || s == KeyStatusWarning
}

// RecordKeySuccess clears the consecutive-failure counter and marks the key
// active. Called after a successful upstream request using this key index.
func (kr *KeyRotator) RecordKeySuccess(credentialID, idx int) {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	states, ok := kr.states[credentialID]
	if !ok || idx < 0 || idx >= len(states) {
		return
	}
	states[idx].consecutiveFailures = 0
	states[idx].totalRequests++
	states[idx].status = KeyStatusActive
	states[idx].lastSuccess = time.Now()
}

// RecordKeyFailure increments the consecutive-failure counter for a key. After
// failureThreshold (2) consecutive failures the key is marked invalid.
// Returns true if this key just became invalid (caller may log/telemetry).
//
// kind guides whether this is a terminal failure: errorsx.KindQuotaPermanent /
// KindQuotaBalance / KindAuthRevoked mark the key terminal immediately (no
// threshold) — these are non-recoverable without operator action (402 balance,
// revoked key).
func (kr *KeyRotator) RecordKeyFailure(credentialID, idx int, kind errorsx.ErrorKind) bool {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	states, ok := kr.states[credentialID]
	if !ok || idx < 0 || idx >= len(states) {
		return false
	}
	states[idx].totalRequests++
	states[idx].totalFailures++
	states[idx].consecutiveFailures++
	states[idx].lastFailure = time.Now()

	// terminal: balance exhausted / key revoked — skip threshold
	if kind == errorsx.KindQuotaPermanent || kind == errorsx.KindQuotaBalance || kind == errorsx.KindAuthRevoked {
		if states[idx].status != KeyStatusTerminal {
			states[idx].status = KeyStatusTerminal
			slog.Info("keyrotator: key marked terminal",
				"credential_id", credentialID, "key_index", idx, "kind", kind)
			return true
		}
		return false
	}
	if states[idx].consecutiveFailures >= failureThreshold {
		if states[idx].status != KeyStatusInvalid {
			states[idx].status = KeyStatusInvalid
			return true
		}
	} else if states[idx].consecutiveFailures > 0 {
		states[idx].status = KeyStatusWarning
	}
	return false
}

// AllKeysInvalid reports whether every key for a credential is invalid or
// terminal (i.e. the credential as a whole is unusable). The executor uses
// this to decide whether to propagate the failure to the credential-level
// circuit breaker (OmniRoute A3 guard: only poison the credential when ALL
// keys are dead, not on a single-key failure).
func (kr *KeyRotator) AllKeysInvalid(credentialID int) bool {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	states, ok := kr.states[credentialID]
	if !ok || len(states) == 0 {
		return false // single-key: caller's existing breaker logic applies
	}
	for _, s := range states {
		if isEligible(s.status) {
			return false
		}
	}
	return true
}

// ResetKey marks a key active again (e.g. after an operator tops up balance or
// a quota window reset). Used by admin reset handlers.
func (kr *KeyRotator) ResetKey(credentialID, idx int) {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	states, ok := kr.states[credentialID]
	if !ok || idx < 0 || idx >= len(states) {
		return
	}
	states[idx].status = KeyStatusActive
	states[idx].consecutiveFailures = 0
}

// KeyCount returns the number of keys registered for a credential (0 if single-key).
func (kr *KeyRotator) KeyCount(credentialID int) int {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	return len(kr.states[credentialID])
}
