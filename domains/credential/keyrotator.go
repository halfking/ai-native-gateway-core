package credential

import (
	"context"
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

// DefaultInvalidCooldown is how long a KeyStatusInvalid key stays out of
// rotation before the sweeper auto-recovers it back to active. Tuned for the
// common transient case: provider-side rate-limit window or a brief 401
// during a key-rotation overlap — typically a few minutes, but operators
// may need up to ~15 minutes for an upstream quota window to roll over.
const DefaultInvalidCooldown = 15 * time.Minute

// DefaultSweepInterval is how often the background sweeper runs to flip
// stale-invalid keys back to active. Small relative to DefaultInvalidCooldown
// so recovered keys become eligible promptly after their cooldown elapses.
const DefaultSweepInterval = 1 * time.Minute

// keyHealth is the in-memory per-key health record. Not persisted; a process
// restart resets all keys to active (intentional, matches OmniRoute behavior).
type keyHealth struct {
	status              KeyStatus
	consecutiveFailures int
	totalRequests       int64
	totalFailures       int64
	lastSuccess         time.Time
	lastFailure         time.Time
	// invalidSince is stamped the first time the key transitions to
	// KeyStatusInvalid. Used by the sweeper to auto-recover stale invalid
	// keys back to active after DefaultInvalidCooldown elapses. Zero for
	// keys that have never been invalid.
	invalidSince time.Time
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

	// sweepOnce guards sweeper goroutine start/stop so StartSweeper is
	// idempotent across calls.
	sweepOnce sync.Once
	// sweepMu guards sweepCancel access in StopSweeper — concurrent stop
	// calls (or a stop racing a fresh start) must not panic or double-cancel.
	sweepMu sync.Mutex
	// sweepCancel is set by StartSweeper to terminate the background sweep
	// goroutine on shutdown. nil if the sweeper was never started or has
	// been stopped.
	sweepCancel context.CancelFunc
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
			states[idx].invalidSince = time.Now()
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
	states[idx].invalidSince = time.Time{}
}

// ResetCredential drops all in-memory key state for a credential. The next
// EnsureCred call rebuilds a fresh dense key set from the current DB extras.
// Use after admin add/delete/status changes, because DB kid_index values may be
// sparse while this rotator intentionally tracks request-time dense slots.
func (kr *KeyRotator) ResetCredential(credentialID int) {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	delete(kr.states, credentialID)
	delete(kr.cursors, credentialID)
}

// KeyCount returns the number of keys registered for a credential (0 if single-key).
func (kr *KeyRotator) KeyCount(credentialID int) int {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	return len(kr.states[credentialID])
}

// SweepInvalid flips every KeyStatusInvalid key whose invalidSince is older
// than cooldown back to KeyStatusActive and zeroes its consecutiveFailures.
// Terminal keys are intentionally NOT touched — they mark non-recoverable
// failures (402 balance, revoked auth) that only an admin ResetKey or
// re-issuance can lift.
//
// now is injected so tests can drive a synthetic clock; cooldown <= 0 uses
// DefaultInvalidCooldown. Returns the number of keys recovered so callers
// can log/meter the event.
func (kr *KeyRotator) SweepInvalid(now time.Time, cooldown time.Duration) int {
	if cooldown <= 0 {
		cooldown = DefaultInvalidCooldown
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	recovered := 0
	for credID, states := range kr.states {
		for idx := range states {
			if states[idx].status != KeyStatusInvalid {
				continue
			}
			if states[idx].invalidSince.IsZero() {
				// Defensive: invalid status without a timestamp (only reachable
				// if a future migration leaves one dangling). Treat as stale
				// so it isn't skipped forever.
				states[idx].invalidSince = now
				continue
			}
			if now.Sub(states[idx].invalidSince) < cooldown {
				continue
			}
			states[idx].status = KeyStatusActive
			states[idx].consecutiveFailures = 0
			states[idx].invalidSince = time.Time{}
			recovered++
			slog.Info("keyrotator: stale invalid key recovered to active",
				"credential_id", credID, "key_index", idx,
				"cooldown", cooldown.String())
		}
	}
	return recovered
}

// StartSweeper launches a background goroutine that periodically calls
// SweepInvalid with DefaultInvalidCooldown / DefaultSweepInterval. The
// sweeper runs until StopSweeper is called or the rotator is replaced.
//
// Idempotent: a second call while the sweeper is already running is a
// no-op. The rotator itself remains safe for concurrent use regardless of
// whether the sweeper is running.
//
// Note: callers that want to restart the sweeper after a stop must construct
// a fresh rotator — KeyRotator is cheap (no I/O), so this is the simplest
// race-free design and matches the rotator's process-lifetime ownership.
func (kr *KeyRotator) StartSweeper(parent context.Context) {
	kr.sweepOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		kr.sweepCancel = cancel
		go kr.sweepLoop(ctx)
	})
}

// StopSweeper terminates the background sweeper if it was started. Safe to
// call from multiple goroutines and idempotent; a no-op when the sweeper
// was never started.
func (kr *KeyRotator) StopSweeper() {
	// Ensure Do's once-handle has run if StartSweeper was never called,
	// so we don't race with a future StartSweeper reading sweepCancel.
	kr.sweepOnce.Do(func() {})
	kr.sweepMu.Lock()
	cancel := kr.sweepCancel
	kr.sweepCancel = nil
	kr.sweepMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (kr *KeyRotator) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(DefaultSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			kr.SweepInvalid(time.Now(), DefaultInvalidCooldown)
		}
	}
}
