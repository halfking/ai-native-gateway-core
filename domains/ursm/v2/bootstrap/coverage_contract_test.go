package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newMiniBootstrap(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()}), mr
}

// seedNode writes a node hash carrying the two fields recovery.ValidateCoverage
// demands, so a member seeded this way is legitimately publishable.
func seedNode(t *testing.T, rdb *redis.Client, key string) {
	t.Helper()
	if err := rdb.HSet(context.Background(), key, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// The 2026-10-04 db14 incident: the manifest was built from the pre-write
// mapped list, so it could claim node states that were never in Redis. The next
// startup's ValidateCoverage then failed and the process degraded to ModeOff.
// Publishing an unbacked promise must be refused at the publishing site.
func TestReplaceCoverageManifestRefusesToOverClaim(t *testing.T) {
	rdb, _ := newMiniBootstrap(t)
	ctx := context.Background()
	const key = "ursm:v2:meta:coverage"

	seedNode(t, rdb, "ursm:v2:node:default:1:model-a")

	// A prior, valid manifest must survive a refused publish.
	if err := replaceCoverageManifest(ctx, rdb, key, []string{"ursm:v2:node:default:1:model-a"}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	err := replaceCoverageManifest(ctx, rdb, key, []string{
		"ursm:v2:node:default:1:model-a",
		"ursm:v2:node:default:2:model-never-written", // never written to Redis
	})
	if err == nil {
		t.Fatal("publishing a manifest with an absent member must be refused")
	}
	if !strings.Contains(err.Error(), "over-claims") {
		t.Fatalf("error must name the defect, got: %v", err)
	}
	if !strings.Contains(err.Error(), "model-never-written") {
		t.Fatalf("error must name the absent key, got: %v", err)
	}

	members, merr := rdb.SMembers(ctx, key).Result()
	if merr != nil {
		t.Fatalf("read manifest: %v", merr)
	}
	if len(members) != 1 || members[0] != "ursm:v2:node:default:1:model-a" {
		t.Fatalf("refused publish must leave the previous manifest intact, got %v", members)
	}
	if _, statErr := rdb.Exists(ctx, key+":staging").Result(); statErr != nil {
		t.Fatalf("staging probe: %v", statErr)
	} else if n, _ := rdb.Exists(ctx, key+":staging").Result(); n != 0 {
		t.Fatalf("refused publish must not leave a staging set behind")
	}
}

// A member that exists but lacks generation/available is just as unusable to
// ValidateCoverage as one that is absent, so it must not be publishable either.
func TestReplaceCoverageManifestRejectsHashMissingRequiredFields(t *testing.T) {
	rdb, _ := newMiniBootstrap(t)
	ctx := context.Background()
	const key = "ursm:v2:meta:coverage"

	const fieldless = "ursm:v2:node:default:9:model-no-fields"
	if err := rdb.HSet(ctx, fieldless, "something_else", "1").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := replaceCoverageManifest(ctx, rdb, key, []string{fieldless}); err == nil {
		t.Fatal("a member without generation/available must not be publishable")
	}
}

// The happy path must keep working: a fully backed manifest is published and
// survives the rename.
func TestReplaceCoverageManifestPublishesFullyBackedManifest(t *testing.T) {
	rdb, _ := newMiniBootstrap(t)
	ctx := context.Background()
	const key = "ursm:v2:meta:coverage"

	seedNode(t, rdb, "ursm:v2:node:default:1:model-a")
	seedNode(t, rdb, "ursm:v2:node:default:2:model-b")

	if err := replaceCoverageManifest(ctx, rdb, key, []string{
		"ursm:v2:node:default:1:model-a",
		"ursm:v2:node:default:2:model-b",
	}); err != nil {
		t.Fatalf("fully backed manifest must publish: %v", err)
	}
	members, err := rdb.SMembers(ctx, key).Result()
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("manifest=%v, want 2 members", members)
	}
}

// presentCoverage is what stops the manifest from being built out of intent.
func TestPresentCoverageKeepsOnlyExistingCandidates(t *testing.T) {
	rdb, _ := newMiniBootstrap(t)
	ctx := context.Background()

	seedNode(t, rdb, "ursm:v2:node:default:1:model-a")
	seedNode(t, rdb, "ursm:v2:node:default:2:model-b")

	got, err := presentCoverage(ctx, rdb, []string{
		"ursm:v2:node:default:1:model-a",
		"ursm:v2:node:default:2:model-b",
		"ursm:v2:node:default:3:model-missing",
	})
	if err != nil {
		t.Fatalf("presentCoverage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("presentCoverage=%v, want the 2 existing keys only", got)
	}
	for _, k := range got {
		if strings.Contains(k, "model-missing") {
			t.Fatalf("absent key leaked into the manifest: %v", got)
		}
	}
}

// presentCoverage must preserve order and handle a batch boundary: the
// pipeline is chunked at 500, and a dropped or reordered element across that
// boundary would silently corrupt the manifest.
func TestPresentCoverageCrossesTheBatchBoundary(t *testing.T) {
	rdb, _ := newMiniBootstrap(t)
	ctx := context.Background()

	const total = 1100 // spans three 500-key chunks
	candidates := make([]string, 0, total)
	for i := 0; i < total; i++ {
		key := "ursm:v2:node:default:" + itoa(i) + ":model"
		seedNode(t, rdb, key)
		candidates = append(candidates, key)
	}
	// One candidate is never written, so it must be the single omission.
	candidates = append(candidates, "ursm:v2:node:default:99999:absent")

	got, err := presentCoverage(ctx, rdb, candidates)
	if err != nil {
		t.Fatalf("presentCoverage: %v", err)
	}
	if len(got) != total {
		t.Fatalf("presentCoverage returned %d keys, want %d", len(got), total)
	}
	for i, key := range got {
		if key != candidates[i] {
			t.Fatalf("presentCoverage reordered at %d: got %q want %q", i, key, candidates[i])
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
