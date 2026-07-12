package executors

import (
	"testing"
	"time"
)

// TestStickyCache_DeleteMultiLevel verifies that credential-fatal errors
// remove all sticky levels immediately.
func TestStickyCache_DeleteMultiLevel(t *testing.T) {
	cache := NewStickyCache()
	appID, apiKeyID := 1, 2

	// Record success at all levels
	cache.RecordSuccessMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123)

	// Verify L1 exists
	lookup := cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4")
	if !lookup.Found || lookup.CredentialID != 123 {
		t.Fatal("L1 should exist after RecordSuccessMultiLevel")
	}

	// Delete all levels
	cache.DeleteMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4")

	// Verify all levels are gone
	lookup = cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4")
	if lookup.Found {
		t.Fatal("all sticky levels should be removed after DeleteMultiLevel")
	}
}

// TestStickyCache_RecordFailureMultiLevel_OnlyMatchesCorrectCredential verifies
// that failure recording only affects entries pointing to the failed credential.
func TestStickyCache_RecordFailureMultiLevel_OnlyMatchesCorrectCredential(t *testing.T) {
	cache := NewStickyCache()
	appID1, apiKeyID1 := 1, 2
	appID2, apiKeyID2 := 3, 4

	// Session1 with credential 123
	cache.RecordSuccessMultiLevel("tenant", &appID1, &apiKeyID1, "profile1", "session1", "gpt-4", 123)
	// Session2 with credential 456 (different client, so L2/L3 won't overlap)
	cache.RecordSuccessMultiLevel("tenant", &appID2, &apiKeyID2, "profile2", "session2", "gpt-4", 456)

	// Record failure for credential 123
	cache.RecordFailureMultiLevel("tenant", &appID1, &apiKeyID1, "profile1", "session1", "gpt-4", 123, 2)
	cache.RecordFailureMultiLevel("tenant", &appID1, &apiKeyID1, "profile1", "session1", "gpt-4", 123, 2)

	// L1 for session1 should be removed
	lookup1 := cache.GetMultiLevel("tenant", &appID1, &apiKeyID1, "profile1", "session1", "gpt-4")
	if lookup1.Found {
		t.Fatal("L1 for session1 should be removed after 2 failures")
	}

	// Session2 should still exist (different credential and client)
	lookup2 := cache.GetMultiLevel("tenant", &appID2, &apiKeyID2, "profile2", "session2", "gpt-4")
	if !lookup2.Found || lookup2.CredentialID != 456 {
		t.Fatal("session2 should remain (different credential and client)")
	}
}

// TestStickyCache_RecordFailureMultiLevel_StateUpdates verifies that
// failure counts are updated correctly even when threshold is not reached.
func TestStickyCache_RecordFailureMultiLevel_StateUpdates(t *testing.T) {
	cache := NewStickyCache()
	appID, apiKeyID := 1, 2

	cache.RecordSuccessMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123)

	// First failure
	reroute := cache.RecordFailureMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123, 2)
	if reroute {
		t.Fatal("first failure should not trigger reroute")
	}

	// Verify entry still exists and failure count is 1
	cache.mu.RLock()
	l1, _, _ := buildStickyKeys("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4")
	e, ok := cache.items[l1]
	cache.mu.RUnlock()

	if !ok {
		t.Fatal("entry should still exist after first failure")
	}
	if e.consecutiveFailures != 1 {
		t.Errorf("expected consecutiveFailures=1, got %d", e.consecutiveFailures)
	}

	// Second failure within window
	time.Sleep(1 * time.Second)
	reroute = cache.RecordFailureMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123, 2)
	if !reroute {
		t.Fatal("second failure should trigger reroute")
	}

	// Entry should be deleted
	lookup := cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4")
	if lookup.Found {
		t.Fatal("entry should be deleted after reaching threshold")
	}
}
