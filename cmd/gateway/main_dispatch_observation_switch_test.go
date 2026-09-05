package main

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// TestJournalEntryToJourneyEvent_SwitchCredWithFromTo verifies that
// NextActionSwitchCred entries with complete from/to fields produce valid
// EventNodeSwitched journey events that pass contract validation.
func TestJournalEntryToJourneyEvent_SwitchCredWithFromTo(t *testing.T) {
	entry := dispatch.JournalEntry{
		Seq:              1,
		At:               time.Now(),
		Model:            "gpt-4o",
		CredentialID:     42,
		ProviderID:       2,
		Vendor:           "openai",
		Action:           dispatch.NextActionSwitchCred,
		ErrorKind:        "rate_limit",
		HTTPStatus:       429,
		Attempt:          2,
		FromCredentialID: 41,
		ToCredentialID:   42,
		FromProviderID:   2,
		ToProviderID:     2,
	}

	event, ok := journalEntryToJourneyEvent("inst-1", "default", "req-123", 0, 0, entry)
	if !ok {
		t.Fatal("journalEntryToJourneyEvent returned ok=false for valid switch_cred entry")
	}

	if event.Type != requestjourney.EventNodeSwitched {
		t.Errorf("Type = %v, want EventNodeSwitched", event.Type)
	}
	if event.FromCredentialID != 41 {
		t.Errorf("FromCredentialID = %d, want 41", event.FromCredentialID)
	}
	if event.ToCredentialID != 42 {
		t.Errorf("ToCredentialID = %d, want 42", event.ToCredentialID)
	}
	// Note: FromProviderID/ToProviderID are tracked in journal but not required
	// by journey contract, so we don't validate them here.

	// Most important: validate must pass
	if err := event.Validate(); err != nil {
		t.Errorf("EventNodeSwitched.Validate() failed: %v", err)
	}
}

// TestJournalEntryToJourneyEvent_SwitchModelWithFromTo verifies that
// NextActionSwitchModel entries with complete from/to fields produce valid
// EventModelSwitched journey events.
func TestJournalEntryToJourneyEvent_SwitchModelWithFromTo(t *testing.T) {
	entry := dispatch.JournalEntry{
		Seq:        2,
		At:         time.Now(),
		Model:      "gpt-4o",
		Action:     dispatch.NextActionSwitchModel,
		ErrorKind:  "no_node",
		HTTPStatus: 0,
		Attempt:    5,
		FromModel:  "glm-5.2",
		ToModel:    "gpt-4o",
	}

	event, ok := journalEntryToJourneyEvent("inst-1", "default", "req-456", 10, 0, entry)
	if !ok {
		t.Fatal("journalEntryToJourneyEvent returned ok=false for valid switch_model entry")
	}

	if event.Type != requestjourney.EventModelSwitched {
		t.Errorf("Type = %v, want EventModelSwitched", event.Type)
	}
	if event.FromModel != "glm-5.2" {
		t.Errorf("FromModel = %q, want glm-5.2", event.FromModel)
	}
	if event.ToModel != "gpt-4o" {
		t.Errorf("ToModel = %q, want gpt-4o", event.ToModel)
	}

	// Validate must pass
	if err := event.Validate(); err != nil {
		t.Errorf("EventModelSwitched.Validate() failed: %v", err)
	}
}

// TestJournalEntryToJourneyEvent_SwitchCredMissingFrom verifies that switch_cred
// entries without FromCredentialID degrade to observation_degraded rather than
// producing invalid EventNodeSwitched that would fail validation.
func TestJournalEntryToJourneyEvent_SwitchCredMissingFrom(t *testing.T) {
	entry := dispatch.JournalEntry{
		Seq:              3,
		At:               time.Now(),
		Model:            "gpt-4o",
		CredentialID:     42,
		ProviderID:       2,
		Action:           dispatch.NextActionSwitchCred,
		Attempt:          3,
		FromCredentialID: 0, // Missing!
		ToCredentialID:   42,
	}

	event, ok := journalEntryToJourneyEvent("inst-1", "default", "req-789", 20, 0, entry)
	if !ok {
		t.Fatal("journalEntryToJourneyEvent returned ok=false")
	}

	// Should degrade rather than emit invalid node_switched
	if event.Type != requestjourney.EventObservationDegraded {
		t.Errorf("Type = %v, want EventObservationDegraded (degraded due to missing from)", event.Type)
	}
	if event.RetryReason != "switch_cred_missing_endpoints" {
		t.Errorf("RetryReason = %q, want switch_cred_missing_endpoints", event.RetryReason)
	}

	// Degraded events should still validate
	if err := event.Validate(); err != nil {
		t.Errorf("Degraded event.Validate() failed: %v", err)
	}
}

// TestJournalEntryToJourneyEvent_SwitchModelMissingFrom verifies that switch_model
// entries without FromModel degrade to observation_degraded.
func TestJournalEntryToJourneyEvent_SwitchModelMissingFrom(t *testing.T) {
	entry := dispatch.JournalEntry{
		Seq:       4,
		At:        time.Now(),
		Model:     "gpt-4o",
		Action:    dispatch.NextActionSwitchModel,
		Attempt:   6,
		FromModel: "", // Missing!
		ToModel:   "gpt-4o",
	}

	event, ok := journalEntryToJourneyEvent("inst-1", "default", "req-abc", 30, 0, entry)
	if !ok {
		t.Fatal("journalEntryToJourneyEvent returned ok=false")
	}

	if event.Type != requestjourney.EventObservationDegraded {
		t.Errorf("Type = %v, want EventObservationDegraded", event.Type)
	}
	if event.RetryReason != "switch_model_missing_endpoints" {
		t.Errorf("RetryReason = %q, want switch_model_missing_endpoints", event.RetryReason)
	}

	if err := event.Validate(); err != nil {
		t.Errorf("Degraded event.Validate() failed: %v", err)
	}
}

// TestJournalEntryToJourneyEvent_SwitchCredFallbackTo verifies that when
// ToCredentialID is 0 but CredentialID is set, the bridge uses CredentialID
// as the ToCredentialID.
func TestJournalEntryToJourneyEvent_SwitchCredFallbackTo(t *testing.T) {
	entry := dispatch.JournalEntry{
		Seq:              5,
		At:               time.Now(),
		Model:            "gpt-4o",
		CredentialID:     99,
		ProviderID:       3,
		Action:           dispatch.NextActionSwitchCred,
		Attempt:          4,
		FromCredentialID: 88,
		ToCredentialID:   0, // Not explicitly set
		FromProviderID:   3,
		ToProviderID:     0,
	}

	event, ok := journalEntryToJourneyEvent("inst-1", "default", "req-fallback", 40, 0, entry)
	if !ok {
		t.Fatal("journalEntryToJourneyEvent returned ok=false")
	}

	if event.Type != requestjourney.EventNodeSwitched {
		t.Errorf("Type = %v, want EventNodeSwitched", event.Type)
	}
	if event.ToCredentialID != 99 {
		t.Errorf("ToCredentialID = %d, want 99 (fallback from CredentialID)", event.ToCredentialID)
	}

	if err := event.Validate(); err != nil {
		t.Errorf("EventNodeSwitched.Validate() failed: %v", err)
	}
}

// TestJournalEntryToJourneyEvent_SwitchModelFallbackTo verifies ToModel fallback.
func TestJournalEntryToJourneyEvent_SwitchModelFallbackTo(t *testing.T) {
	entry := dispatch.JournalEntry{
		Seq:       6,
		At:        time.Now(),
		Model:     "gpt-4-turbo",
		Action:    dispatch.NextActionSwitchModel,
		Attempt:   7,
		FromModel: "glm-5.2",
		ToModel:   "", // Not explicitly set
	}

	event, ok := journalEntryToJourneyEvent("inst-1", "default", "req-model-fb", 50, 0, entry)
	if !ok {
		t.Fatal("journalEntryToJourneyEvent returned ok=false")
	}

	if event.Type != requestjourney.EventModelSwitched {
		t.Errorf("Type = %v, want EventModelSwitched", event.Type)
	}
	if event.ToModel != "gpt-4-turbo" {
		t.Errorf("ToModel = %q, want gpt-4-turbo (fallback from Model)", event.ToModel)
	}

	if err := event.Validate(); err != nil {
		t.Errorf("EventModelSwitched.Validate() failed: %v", err)
	}
}
