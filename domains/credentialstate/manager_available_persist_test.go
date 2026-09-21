package credentialstate

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestUpdateOnFailure_PersistsAvailableAndRecoverAt verifies that
// UpdateOnFailure writes Available and RecoverAt to the batch writer,
// fixing the asymmetry where UpdateOnSuccess writes Available=true but
// UpdateOnFailure didn't write Available=false or RecoverAt.
//
// Background: this was causing credential cooling to work in memory but not
// persist to the database, leading to recovery failures and premature
// route-incident visibility (2026-09-16 apiclaude/apigpt/suyun case).
//
// This test uses reflection to intercept BatchWriter.Add calls.
func TestUpdateOnFailure_PersistsAvailableAndRecoverAt(t *testing.T) {
	// Create a real BatchWriter with nil pool (won't flush to DB)
	writer := &BatchWriter{
		db:        nil, // nil pool prevents actual DB writes
		buffer:    make([]StateUpdate, 0, 100),
		interval:  time.Hour,
		batchSize: 100,
		done:      make(chan struct{}),
	}

	mgr := &Manager{
		batchWriter:          writer,
		memCache:             &sync.Map{},
		memCacheTTL:          10 * time.Second,
		activeProbeThreshold: 2,
		pendingTimers:        make(map[string]*pendingProbeTimer),
	}

	ctx := context.Background()

	// Trigger 3 consecutive transient failures (paid credential)
	// This should set Available=false and RecoverAt after the 3rd failure
	for i := 0; i < 3; i++ {
		mgr.UpdateOnFailure(ctx, 123, "gpt-4", errorsx.KindTimeout, "req-"+string(rune('1'+i)), "tenant-test", "paid")
	}

	// Inspect the writer's buffer
	writer.bufferMu.Lock()
	captured := append([]StateUpdate(nil), writer.buffer...)
	writer.bufferMu.Unlock()

	if len(captured) != 3 {
		t.Fatalf("expected 3 updates, got %d", len(captured))
	}

	// The 3rd update should have Available=false and RecoverAt set
	lastUpdate := captured[2]

	if lastUpdate.Available == nil {
		t.Fatal("Available field not written on failure (asymmetry bug)")
	}
	if *lastUpdate.Available != false {
		t.Fatalf("Available should be false after 3 transient failures, got %v", *lastUpdate.Available)
	}

	if lastUpdate.RecoverAt == nil {
		t.Fatal("RecoverAt field not written on cooling (asymmetry bug)")
	}

	// RecoverAt should be ~5 minutes in the future (transient cooling)
	expectedRecoverAt := time.Now().Add(5 * time.Minute)
	diff := lastUpdate.RecoverAt.Sub(expectedRecoverAt)
	if diff < -10*time.Second || diff > 10*time.Second {
		t.Fatalf("RecoverAt should be ~5min from now, got diff %v", diff)
	}
}

// TestUpdateOnFailure_FreeCredentialTransient_StaysAvailable verifies that
// free credentials with transient errors stay Available=true.
func TestUpdateOnFailure_FreeCredentialTransient_StaysAvailable(t *testing.T) {
	writer := &BatchWriter{
		db:        nil,
		buffer:    make([]StateUpdate, 0, 100),
		interval:  time.Hour,
		batchSize: 100,
		done:      make(chan struct{}),
	}

	mgr := &Manager{
		batchWriter:          writer,
		memCache:             &sync.Map{},
		memCacheTTL:          10 * time.Second,
		activeProbeThreshold: 2,
		pendingTimers:        make(map[string]*pendingProbeTimer),
	}

	ctx := context.Background()

	// 3 consecutive transient failures on a FREE credential
	for i := 0; i < 3; i++ {
		mgr.UpdateOnFailure(ctx, 456, "gpt-3.5-turbo", errorsx.KindTimeout, "req-"+string(rune('1'+i)), "tenant-free", "free")
	}

	writer.bufferMu.Lock()
	captured := append([]StateUpdate(nil), writer.buffer...)
	writer.bufferMu.Unlock()

	if len(captured) != 3 {
		t.Fatalf("expected 3 updates, got %d", len(captured))
	}

	// Free credential should stay Available=true even after 3 failures
	lastUpdate := captured[2]

	if lastUpdate.Available == nil {
		t.Fatal("Available field not written")
	}
	if *lastUpdate.Available != true {
		t.Fatalf("Free credential should stay Available=true on transient failures, got %v", *lastUpdate.Available)
	}

	// RecoverAt should NOT be set for free credentials (no cooling)
	if lastUpdate.RecoverAt != nil {
		t.Fatalf("Free credential should not have RecoverAt set (no cooling), got %v", lastUpdate.RecoverAt)
	}
}

// TestUpdateOnFailure_PermanentError_SetsRecoverAt verifies permanent
// errors set a 15-minute RecoverAt.
func TestUpdateOnFailure_PermanentError_SetsLongRecoverAt(t *testing.T) {
	writer := &BatchWriter{
		db:        nil,
		buffer:    make([]StateUpdate, 0, 100),
		interval:  time.Hour,
		batchSize: 100,
		done:      make(chan struct{}),
	}

	mgr := &Manager{
		batchWriter:          writer,
		memCache:             &sync.Map{},
		memCacheTTL:          10 * time.Second,
		activeProbeThreshold: 2,
		pendingTimers:        make(map[string]*pendingProbeTimer),
	}

	ctx := context.Background()

	// 2 consecutive auth failures (permanent)
	mgr.UpdateOnFailure(ctx, 789, "claude-3", errorsx.KindAuth, "req-1", "tenant-test", "paid")
	mgr.UpdateOnFailure(ctx, 789, "claude-3", errorsx.KindAuth, "req-2", "tenant-test", "paid")

	writer.bufferMu.Lock()
	captured := append([]StateUpdate(nil), writer.buffer...)
	writer.bufferMu.Unlock()

	if len(captured) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(captured))
	}

	lastUpdate := captured[1]

	if lastUpdate.Available == nil || *lastUpdate.Available != false {
		t.Fatal("Available should be false after 2 permanent failures")
	}

	if lastUpdate.RecoverAt == nil {
		t.Fatal("RecoverAt should be set for permanent failures")
	}

	// RecoverAt should be ~15 minutes in the future (permanent cooling)
	expectedRecoverAt := time.Now().Add(15 * time.Minute)
	diff := lastUpdate.RecoverAt.Sub(expectedRecoverAt)
	if diff < -10*time.Second || diff > 10*time.Second {
		t.Fatalf("RecoverAt should be ~15min from now for permanent error, got diff %v", diff)
	}
}

// Suppress unused import warning
var _ = reflect.TypeOf(0)
