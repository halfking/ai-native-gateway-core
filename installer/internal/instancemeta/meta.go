// Package instancemeta reads {installDir}/state/activation.json and exposes
// the per-instance identity + activation snapshot (install_mode, instance_id,
// ip_address, etc.) to the launcher web UI.
//
// The activation.json file is written by the activation/enrollment subsystem
// (agent B) after wizard completes. This package only READS it — it never
// writes, modifies, or migrates the file. Loader semantics:
//
//   - File missing              → zero InstanceMeta + os.ErrNotExist
//     (caller logs Warn and treats the meta block as empty)
//   - File present, JSON valid  → populated InstanceMeta, nil
//   - File present, JSON broken → zero InstanceMeta + non-nil error
//     (caller logs Warn, never panics, never blocks launcher startup)
//
// The schema is intentionally permissive: unknown fields are ignored so
// agent B can add new keys (e.g. license_tier) without breaking the
// launcher UI rendering.
package instancemeta

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// FileName is the on-disk filename under {installDir}/state/.
const FileName = "activation.json"

// InstaFile mirrors the JSON layout written by the activation/enrollment
// pipeline. Field names match the wire format (snake_case). All fields are
// optional — partial files (e.g. mode set but ip_address still empty because
// the device hasn't been seen by master yet) are decoded without complaint.
type InstaFile struct {
	InstanceID    string `json:"instance_id,omitempty"`
	InstanceToken string `json:"instance_token,omitempty"`
	DeviceCode    string `json:"device_code,omitempty"`
	IPAddress     string `json:"ip_address,omitempty"`
	Mode          string `json:"mode,omitempty"`      // "full" | "lite" | "unknown"
	Status        string `json:"status,omitempty"`    // "activated"|"trial"|"skipped"|"failed"|"unknown"
	ActivatedAt   string `json:"activated_at,omitempty"`
	Error         string `json:"error,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

// Load reads {installDir}/state/activation.json and returns the populated
// InstanceMeta. See package doc for the three error cases.
//
// installDir is the gateway install directory (parent of the state/ dir).
// Pass the empty string if the caller hasn't resolved it yet — Load treats
// that as "no file" and returns os.ErrNotExist without attempting I/O.
func Load(installDir string) (Meta, error) {
	if installDir == "" {
		return Meta{}, os.ErrNotExist
	}
	path := filepath.Join(installDir, "state", FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing file is the common case for fresh installs / lite mode
		// / upgrade scenarios where the wizard hasn't yet produced
		// activation.json. Return ErrNotExist verbatim so callers can
		// distinguish "not yet provisioned" from "broken JSON".
		return Meta{}, err
	}

	var f InstaFile
	if err := json.Unmarshal(data, &f); err != nil {
		// JSON corruption must NOT crash the launcher. Caller logs
		// Warn and treats the meta block as empty (web UI shows —).
		return Meta{}, err
	}
	return Meta{
		InstallMode:      f.Mode,
		InstanceID:       f.InstanceID,
		DeviceCode:       f.DeviceCode,
		IPAddress:        f.IPAddress,
		ActivationStatus: f.Status,
		ActivationError:  f.Error,
		ActivatedAt:      f.ActivatedAt,
	}, nil
}

// Meta is the public, transport-friendly shape of InstanceMeta. The api
// package depends on this type so it doesn't have to import the file-level
// schema. Kept separate from api.InstanceMeta to allow the API contract to
// evolve independently of the on-disk JSON schema.
type Meta struct {
	InstallMode      string
	InstanceID       string
	DeviceCode       string
	IPAddress        string
	ActivationStatus string
	ActivationError  string
	ActivatedAt      string
}

// IsZero reports whether the meta has any user-visible content. Useful for
// callers that want to decide "should we even surface the card?".
func (m Meta) IsZero() bool {
	return m.InstallMode == "" &&
		m.InstanceID == "" &&
		m.DeviceCode == "" &&
		m.IPAddress == "" &&
		m.ActivationStatus == "" &&
		m.ActivationError == "" &&
		m.ActivatedAt == ""
}