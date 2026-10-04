package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// These are the behavioural gates for the contract the 2026-10-04 db14 incident
// broke. The source-level wiring gates in cmd/gateway prove main.go hands the
// same client to both consumers; they cannot prove that what bootstrap
// publishes is something coverage validation will actually accept. Only running
// both sides can.
//
// The load-bearing invariant, stated once:
//
//	everything bootstrap publishes must be accepted by ValidateCoverage
//
// Before the fix the manifest was assembled from what PostgreSQL *claimed*
// should exist, so it could carry keys that were never in Redis. The next
// startup's ValidateCoverage then failed and the process degraded to ModeOff —
// with no error anywhere pointing at the manifest.

// seedHashNode writes a node hash carrying the two fields ValidateCoverage reads.
func seedHashNode(t *testing.T, rdb *redis.Client, key string) {
	t.Helper()
	if err := rdb.HSet(context.Background(), key, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

const (
	nodePresent = "ursm:v2:node:default:1:model-a"
	nodeMissing = "ursm:v2:node:default:2:model-never-written"
)

// The negative direction comes first on purpose. A gate that only asserts the
// happy path passes for a detector that never fires, so the test has to show
// itself failing before it is allowed to claim the fix works.
func TestCoverageValidationRejectsAManifestThatOverClaims(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	seedHashNode(t, rdb, nodePresent)

	// Publish the pre-fix behaviour: the full intended list, unbacked member
	// included, straight from the staging set to the live manifest.
	tmp := "ursm:v2:meta:coverage:staging"
	if err := rdb.SAdd(ctx, tmp, nodePresent, nodeMissing).Err(); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := rdb.Rename(ctx, tmp, "ursm:v2:meta:coverage").Err(); err != nil {
		t.Fatalf("rename: %v", err)
	}

	rm := recovery.New(rdb, "ursm:v2:")
	rm.SetKeySchemaMode(store.KeySchemaModeLegacy)

	_, err := rm.ValidateCoverage(ctx)
	if !errors.Is(err, recovery.ErrCoverageIncomplete) {
		t.Fatalf("an over-claiming manifest must be refused with ErrCoverageIncomplete, got %v", err)
	}
	// The sentinel alone does not discriminate: for a wholly absent key the
	// HMGET field check would also report ErrCoverageIncomplete, so removing
	// the EXISTS check is an equivalent mutant at this level. The distinction
	// is worth pinning because the message is what identifies *which* of the
	// two checks fired — and on 2026-10-04 that line was the only evidence
	// separating "the key is gone" from "the hash is incomplete".
	if !strings.Contains(err.Error(), "coverage key missing") {
		t.Fatalf("an absent key must be reported as 'coverage key missing', not the field check: %v", err)
	}
}

// The positive direction: the fixed publish path produces a manifest that
// coverage validation accepts, even when a candidate could not be backed.
func TestPublishedManifestIsAcceptedByCoverageValidation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	seedHashNode(t, rdb, nodePresent)

	// The same two candidates the negative case uses.
	candidates := []string{nodePresent, nodeMissing}
	present, err := presentCoverage(ctx, rdb, candidates)
	if err != nil {
		t.Fatalf("presentCoverage: %v", err)
	}
	if err := replaceCoverageManifest(ctx, rdb, "ursm:v2:meta:coverage", present); err != nil {
		t.Fatalf("publish: %v", err)
	}

	rm := recovery.New(rdb, "ursm:v2:")
	rm.SetKeySchemaMode(store.KeySchemaModeLegacy)

	count, err := rm.ValidateCoverage(ctx)
	if err != nil {
		t.Fatalf("what bootstrap published must satisfy ValidateCoverage, got %v", err)
	}
	if count != 1 {
		t.Fatalf("validated %d members, want 1 (only the backed key may be claimed)", count)
	}
}

// The production failure in miniature: writes land on one Redis database while
// validation reads another, and no amount of retrying or re-publishing the same
// keys can make them converge. This is why the fix had to be about *which
// client* is passed, not about copying keys more carefully or running a second
// pass.
func TestCoverageNeverConvergesAcrossTwoDatabases(t *testing.T) {
	gateway := miniredis.RunT(t) // stands in for the shared db2
	ursm := miniredis.RunT(t)    // stands in for the dedicated URSM db

	gatewayRDB := redis.NewClient(&redis.Options{Addr: gateway.Addr()})
	ursmRDB := redis.NewClient(&redis.Options{Addr: ursm.Addr()})
	ctx := context.Background()

	// The manifest itself was copied across, so validation sees a populated set.
	seedHashNode(t, ursmRDB, nodePresent)
	if err := ursmRDB.SAdd(ctx, "ursm:v2:meta:coverage", nodePresent).Err(); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	// Bootstrap keeps writing into the gateway db, which is the whole bug.
	seedHashNode(t, gatewayRDB, nodePresent)
	present, err := presentCoverage(ctx, gatewayRDB, []string{nodePresent})
	if err != nil {
		t.Fatalf("presentCoverage: %v", err)
	}
	if len(present) != 1 {
		t.Fatalf("gateway db write should have succeeded locally, got %v", present)
	}

	// Validation, pointed at the URSM db, must still refuse — and re-running
	// the identical publish cannot change that.
	rm := recovery.New(ursmRDB, "ursm:v2:")
	rm.SetKeySchemaMode(store.KeySchemaModeLegacy)
	if _, err := rm.ValidateCoverage(ctx); err != nil {
		t.Fatalf("fixture is wrong: %v", err)
	}

	// Now break the URSM db's copy of the node while the manifest still names
	// it — the exact state a restart would find, and the state that degraded
	// 154 on every attempt.
	if err := ursmRDB.Del(ctx, nodePresent).Err(); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	for i := 0; i < 3; i++ {
		// Republishing into the wrong database is what the deployment did.
		if _, err := presentCoverage(ctx, gatewayRDB, []string{nodePresent}); err != nil {
			t.Fatalf("attempt %d: presentCoverage: %v", i+1, err)
		}
		if _, err := rm.ValidateCoverage(ctx); !errors.Is(err, recovery.ErrCoverageIncomplete) {
			t.Fatalf("attempt %d: coverage must stay broken while writes target another db, got %v", i+1, err)
		}
	}
}
