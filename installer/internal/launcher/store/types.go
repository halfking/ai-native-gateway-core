package store

import "time"

// Release describes a Gateway version available for upgrade.
type Release struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Changelog   string `json:"changelog,omitempty"`
}

// State machine constants for blue-green upgrade plans.
const (
	StateNotified   = "NOTIFIED"    // checker found new version, awaiting operator Prepare
	StatePreparing  = "PREPARING"   // downloading + staging green + migrating
	StatePrepared   = "PREPARED"    // green healthy + migration applied, awaiting operator Apply
	StateActivating = "ACTIVATING"  // proxy switching to green
	StateDraining   = "DRAINING"    // blue graceful drain
	StateDone       = "DONE"        // green is now active, blue retained
	StateFailed     = "FAILED"      // any phase failed
	StateRolledBack = "ROLLED_BACK" // operator triggered rollback
)

// StateEvent records a state transition for audit/debug.
type StateEvent struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
	Note  string    `json:"note,omitempty"`
}

// AuditEntry records operator actions (apply, rollback, prepare).
type AuditEntry struct {
	Action string    `json:"action"` // "apply" | "rollback" | "prepare"
	Who    string    `json:"who"`    // token identifier
	At     time.Time `json:"at"`
}
