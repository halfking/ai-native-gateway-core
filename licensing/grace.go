package licensing

// Grace-period semantics for license enforcement.
//
// Why grace period?
// In a production deployment, the license server may be temporarily
// unreachable (network blip, master rolling restart, etc.). An
// immediate hard-fail at startup would enter restricted mode and
// disrupt traffic even though the prior instance_token is still valid
// for up to 7 days.
//
// The grace-period design (added v2) lets the operator specify a
// window during which license verification failures are tolerated:
//   - On a temporary blip, the failure marker is updated but the
//     service continues serving until the marker is older than the
//     grace window.
//   - On an authentic revocation or signature break, the operator
//     can short-circuit by deleting the marker file or setting
//     `LICENSE_NO_GRACE=1`.
//
// State machine:
//
//   Pass:  no failure marker  →  normal mode
//   Fail:  failure marker (timestamp)  →  grace mode for up to
//                                      grace window, then restricted
//
// The marker is stored under dataDir as "license_failure_marker"
// alongside other license state so a single chown wipes them all.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultGracePeriod is the fallback when EnforceAtStartupWithGrace
// is called without an explicit grace — 24 hours matches the
// instance_token lifetime slack (7 days) so transient master outages
// don't bump traffic into restricted mode.
const DefaultGracePeriod = 24 * time.Hour

// failureMarkerName is the filename inside dataDir storing the last
// verification-failure timestamp. Format: {"failed_at":"<RFC3339>"}.
const failureMarkerName = "license_failure_marker.json"

// FailureMarker is the on-disk JSON record of the last failed
// enforcement check. Kept deliberately small so an operator can
// `cat` it during an incident.
type FailureMarker struct {
	FailedAt time.Time `json:"failed_at"`
	Reason   string    `json:"reason,omitempty"`
	Source   string    `json:"source,omitempty"` // last license file path tried
	Attempts int       `json:"attempts,omitempty"`
}

// IsOlderThan reports whether the marker is older than d.
// Used by grace calculation: marker older than grace → restricted.
func (m FailureMarker) IsOlderThan(d time.Duration) bool {
	if m.FailedAt.IsZero() {
		return false
	}
	return time.Since(m.FailedAt) > d
}

// Marshal marshals the marker for atomic file write.
func (m FailureMarker) Marshal() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// UnmarshalMarker parses an existing marker file. Returns a zero-value
// marker + nil error if the file does not exist (first failure flow).
func UnmarshalMarker(data []byte) (FailureMarker, error) {
	if len(data) == 0 {
		return FailureMarker{}, nil
	}
	var m FailureMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return FailureMarker{}, fmt.Errorf("marker parse: %w", err)
	}
	return m, nil
}

// markFailure writes the failure marker atomically. Updates the
// previous marker's AttemptCount if one existed, and propagates the
// most recent Reason for forensic visibility.
func markFailure(dataDir, reason, source string) (FailureMarker, error) {
	var marker FailureMarker

	path := filepath.Join(dataDir, failureMarkerName)
	if data, err := os.ReadFile(path); err == nil {
		// Best-effort merge: preserve attempt count.
		if prev, perr := UnmarshalMarker(data); perr == nil {
			marker = prev
		}
	}
	marker.FailedAt = time.Now().UTC()
	marker.Reason = reason
	marker.Source = source
	marker.Attempts++

	out, err := marker.Marshal()
	if err != nil {
		return marker, fmt.Errorf("marshal marker: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return marker, fmt.Errorf("write marker tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return marker, fmt.Errorf("rename marker: %w", err)
	}
	return marker, nil
}

// MarkLicenseFailure is the public entry for callers (e.g. offline_cron)
// that need to record a license verification failure for grace tracking.
// Idempotent: safe to call multiple times; Attempt counter increments.
func MarkLicenseFailure(dataDir, reason, source string) error {
	if dataDir == "" {
		return errors.New("dataDir must not be empty")
	}
	_, err := markFailure(dataDir, reason, source)
	return err
}

// ClearLicenseFailure removes the failure marker (called after a
// successful re-verification to reset the grace clock).
func ClearLicenseFailure(dataDir string) error {
	if dataDir == "" {
		return errors.New("dataDir must not be empty")
	}
	return os.Remove(filepath.Join(dataDir, failureMarkerName))
}

// ReadLicenseFailure returns the current marker (zero-value if no
// marker exists). Safe to call concurrently.
func ReadLicenseFailure(dataDir string) (FailureMarker, error) {
	if dataDir == "" {
		return FailureMarker{}, errors.New("dataDir must not be empty")
	}
	data, err := os.ReadFile(filepath.Join(dataDir, failureMarkerName))
	if os.IsNotExist(err) {
		return FailureMarker{}, nil
	}
	if err != nil {
		return FailureMarker{}, err
	}
	return UnmarshalMarker(data)
}

// GracePolicy decides whether the enforcement result should fail
// closed or hold over on the basis of an existing marker + grace
// duration.
type GracePolicy struct {
	// GraceDuration is the window during which a verification
	// failure is tolerated before restricted mode kicks in.
	GraceDuration time.Duration

	// Now is the override point for tests. Production callers leave
	// this zero-valued so time.Now() is used.
	Now func() time.Time
}

// EnforceWithGrace runs VerifyLocalLicense and applies the grace policy:
//   - Success: clear the marker, return nil
//   - Failure + no prior marker: fail-closed (enforce immediately)
//   - Failure + prior marker within GraceDuration: tolerate (grace)
//   - Failure + prior marker older than GraceDuration: fail-closed
//
// Operators can opt out of grace entirely by setting
// LICENSE_NO_GRACE=1 in the process environment — useful when
// security incidents need an immediate revoke.
func (p GracePolicy) EnforceWithGrace(
	dataDir string,
	verify func() error,
) error {
	if dataDir == "" {
		return errors.New("dataDir must not be empty")
	}

	// Honour LICENSE_NO_GRACE: hard fail on any failure, no grace.
	if os.Getenv("LICENSE_NO_GRACE") == "1" {
		if err := verify(); err != nil {
			return fmt.Errorf("LICENSE_NO_GRACE: %w", err)
		}
		return ClearLicenseFailure(dataDir)
	}

	now := p.Now
	if now == nil {
		now = time.Now
	}

	err := verify()
	if err == nil {
		// Success: clear any prior marker.
		_ = ClearLicenseFailure(dataDir)
		return nil
	}

	// Verify failed. Determine grace based on the EXISTING marker —
	// a fresh write would reset FailedAt and break the comparison.
	prior, _ := ReadLicenseFailure(dataDir)
	if prior.FailedAt.IsZero() {
		// No prior marker — first-ever failure. Write the marker
		// (records the problem for forensics) and immediate hard-fail
		// even though we're inside the grace window: a brand-new
		// instance that's never validated should not silently ride
		// the grace window on day 1.
		_, _ = markFailure(dataDir, err.Error(), "")
		return fmt.Errorf("license verification failed (first failure, no grace): %w", err)
	}

	// Update the marker (preserves FailedAt-attempts counter is
	// wrong here — we need to keep FailedAt as the original so the
	// grace calculation is anchored to "first observed failure").
	// Refuse to touch FailedAt; just bump Attempts + Reason.
	writeMarker := prior
	writeMarker.Attempts++
	writeMarker.Reason = err.Error()
	if out, mErr := writeMarker.Marshal(); mErr == nil {
		_ = os.WriteFile(filepath.Join(dataDir, failureMarkerName), out, 0o600)
	}

	age := now().Sub(prior.FailedAt)
	if age < p.GraceDuration {
		// Within grace: tolerate the failure but still report it
		// so callers can log/meter.
		return ErrLicenseInGracePeriod
	}
	// Past grace: hard-fail.
	return fmt.Errorf("license verification failed (grace exceeded, age=%s): %w",
		age.Round(time.Second), err)
}

// ErrLicenseInGracePeriod indicates the verify call failed but the
// grace window is still active. Callers may log this and continue
// serving, but it MUST eventually be cleared via ClearLicenseFailure
// after the underlying cause is fixed (e.g. master reconnects).
var ErrLicenseInGracePeriod = errors.New("license verification failed within grace period")

// graceMu guards concurrent access to the marker file. We need this
// because EnforceWithGrace may be called from the
// startup goroutine and the periodic daemon concurrently.
var graceMu sync.Mutex

// EnforceAtStartupWithGrace bundles verify + grace in one call:
//   - licensePath / publicKeyPath used for the verify call
//   - dataDir used as the marker file location
//   - grace chosen as the tolerance window
func EnforceAtStartupWithGrace(
	licensePath, publicKeyPath, dataDir string,
	grace time.Duration,
) error {
	if grace <= 0 {
		grace = DefaultGracePeriod
	}
	graceMu.Lock()
	defer graceMu.Unlock()

	p := GracePolicy{GraceDuration: grace}
	return p.EnforceWithGrace(dataDir, func() error {
		pubPEM, err := os.ReadFile(publicKeyPath)
		if err != nil {
			return fmt.Errorf("load public key: %w", err)
		}
		if _, err := VerifyLocalLicense(licensePath, pubPEM, dataDir); err != nil {
			return fmt.Errorf("verify license: %w", err)
		}
		return nil
	})
}
