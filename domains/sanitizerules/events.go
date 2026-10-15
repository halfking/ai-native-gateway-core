package sanitizerules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

func prepareEvent(e Event) (Event, error) {
	if !validID(e.RuleID) || (e.TenantID != "" && !validID(e.TenantID)) || e.Version < 0 {
		return Event{}, fmt.Errorf("%w: invalid event identifiers", ErrInvalid)
	}
	if e.Phase != "input" && e.Phase != "output" && e.Phase != "egress" {
		return Event{}, fmt.Errorf("%w: invalid event phase", ErrInvalid)
	}
	switch e.Action {
	case "mask", "block", "log", "exempted", "pending":
	default:
		return Event{}, fmt.Errorf("%w: invalid event action", ErrInvalid)
	}
	// Build from a closed vocabulary only. Request text, arbitrary masked samples,
	// detector captures and field values never enter the persisted representation.
	e.Type = structuralType(e.Type)
	e.Field = structuralField(e.Field)
	key, _ := json.Marshal([]any{e.TenantID, e.RuleID, e.Version, e.Phase, e.Action, e.Type, e.Field})
	sum := sha256.Sum256(key)
	now := time.Now().UTC()
	return Event{Fingerprint: hex.EncodeToString(sum[:]), TenantID: e.TenantID, RuleID: e.RuleID, Version: e.Version, Phase: e.Phase, Action: e.Action, Type: e.Type, Field: e.Field, Status: "pending", SampleMasked: "[REDACTED:" + e.Type + "] field=" + e.Field + " phase=" + e.Phase + " action=" + e.Action, Count: 1, FirstSeen: now, LastSeen: now}, nil
}
func validReview(status, reviewer, comment string) error {
	if status != "confirmed_sensitive" && status != "false_positive" && status != "ignored" {
		return fmt.Errorf("%w: invalid review status", ErrInvalid)
	}
	if reviewer == "" || len(reviewer) > 256 || len(comment) > 2000 {
		return fmt.Errorf("%w: invalid reviewer or comment length", ErrInvalid)
	}
	return nil
}
func boundedLimit(n int) int {
	if n <= 0 {
		return 100
	}
	if n > 1000 {
		return 1000
	}
	return n
}
func retentionLimit(n int) int {
	if n < 1 {
		return 10000
	}
	if n > 1000000 {
		return 1000000
	}
	return n
}
func safeInstance(i Instance) Instance {
	if i.Error != "" {
		i.Error = "rule refresh or heartbeat failed"
	}
	return i
}

// These vocabularies describe structure only. Arbitrary JSON keys and detector
// labels may themselves contain a secret, so they must never enter evidence.
func structuralType(v string) string {
	switch v {
	case "phone", "id_card", "email", "credit_card", "secret", "account", "server_ip", "internal_ip", "custom":
		return v
	}
	return "custom"
}
func structuralField(v string) string {
	switch v {
	case "pair_slash", "pair_pipe", "pair_colon", "pair_other", "credential_pair", "credential_declaration", "account_declaration", "credential_assignment", "account_assignment", "vendor_key_shape", "pattern", "password", "command", "content", "text", "arguments", "authorization", "token", "api_key":
		return v
	}
	return "text"
}
