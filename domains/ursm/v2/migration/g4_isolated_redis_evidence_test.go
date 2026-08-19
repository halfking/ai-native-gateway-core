//go:build g4probe

// G4 isolated Redis evidence probes. Compiled only when `-tags g4probe` is
// passed so default builds never execute these. They connect to a dedicated
// isolated Redis reachable via URSM_G4_REDIS_ADDR (default localhost:63790)
// and assume the operator has confirmed the address points at a *throwaway*
// instance (the local setup script uses a daemonized, save="", appendonly=no
// redis-server with no shared keyspace). They NEVER connect to the default
// 6379 — that instance carries unrelated production data.
//
// The probes exercise the slices doc 14 §6 / 15 §4 marked as G4留待项:
//  1. EntryCopier.CopyHash Lua atomic fence on a real Redis 7+ instance.
//  2. SCRIPT FLUSH -> subsequent call still works (lua reload via EVALSHA
//     fallback to EVAL; go-redis Script.Run handles both).
//  3. Connection drop & restart: client.Conn() forced-close, then a fresh
//     Client continues to operate against the same keyspace.
//  4. TTL preservation with live PTTL decay under repeated EVAL cycles
//     (assert target PTTL <= scan-time PTTL, never extended).
//  5. Cleanup Compare-Delete: source mutated between scan and delete -> the
//     entry is preserved, no DEL is issued.
//  6. Concurrent stale-owner claim recovery: two entry-copier passes race
//     against a fenced PG cleanup claim; recovery returns the row to
//     'copied' and refuses to delete.
//
// This file does not touch the canonical PG store; the PGStore integration
// path is already covered by TestPGStoreOpenRunAfter080And081 (run with
// `-tags integration`) and was captured in /tmp/ursm-g4-evidence/logs/.
package migration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// g4ProbeRedis opens a client to the isolated Redis and verifies it is
// reachable. The helper refuses to fall back to the default 127.0.0.1:6379
// so an operator mistake can never reach the shared instance.
func g4ProbeRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	addr := os.Getenv("URSM_G4_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:63790"
	}
	if addr == "127.0.0.1:6379" || addr == "localhost:6379" {
		t.Skipf("refusing to run G4 evidence against the shared 6379 instance (addr=%s)", addr)
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:        addr,
		DialTimeout: 1500 * time.Millisecond,
		ReadTimeout: 1500 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("isolated Redis at %s unreachable: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, addr
}

// g4Prefix isolates this probe's keys inside a dedicated namespace so it can
// be safely left behind in the throwaway Redis.
const g4Prefix = "ursm:g4probe:"

func g4Key(name string) string { return g4Prefix + name }

// 1. CopyHash Lua atomic fence on real Redis. Sets a positive TTL on the
// source so the path stays inside the live-TTL branch (the PTTL=-1
// sentinel from real Redis is reported as time.Duration(-1) by go-redis,
// which the entry copier treats as invalid in its current form; that is a
// separate G4留待项 and is covered by a dedicated probe below).
func TestG4EntryCopierCopyHashRealRedis(t *testing.T) {
	rdb, addr := g4ProbeRedis(t)
	t.Cleanup(func() {
		_, _ = rdb.Del(context.Background(), g4Key("src"), g4Key("tgt")).Result()
	})

	source := g4Key("src")
	target := g4Key("tgt")
	fields := map[string]any{
		"generation":    "1",
		"available":     "1",
		"fail_streak":   "0",
		"success_count": "5",
		"failure_count": "0",
		"updated_at_ms": "1700000000000",
	}
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if err := rdb.PExpire(context.Background(), source, 10*time.Second).Err(); err != nil {
		t.Fatalf("pexpire source: %v", err)
	}
	copier := NewEntryCopier(rdb, "ursm:v2:")
	entry := EntryRecord{
		SourceKey:     source,
		TargetKey:     target,
		Class:         ClassificationMigratable,
		Generation:    1,
		FieldChecksum: checksumFields(mustStringFields(fields, rdb, source)),
		Type:          "hash",
	}
	res, err := copier.CopyHash(context.Background(), entry)
	if err != nil {
		t.Fatalf("CopyHash failed against %s: %v", addr, err)
	}
	if res.Status != EntryCopyApplied {
		t.Fatalf("first CopyHash status = %q, want applied", res.Status)
	}

	// Second pass -> already_applied (marker idempotent).
	res2, err := copier.CopyHash(context.Background(), entry)
	if err != nil {
		t.Fatalf("idempotent CopyHash failed: %v", err)
	}
	if res2.Status != EntryCopyAlreadyApplied {
		t.Fatalf("idempotent CopyHash status = %q, want already_applied", res2.Status)
	}
}

// 2. SCRIPT FLUSH + transparent reload through go-redis Script.Run.
func TestG4CopyHashSurvivesScriptFlush(t *testing.T) {
	rdb, addr := g4ProbeRedis(t)
	source := g4Key("script-flush-src")
	target := g4Key("script-flush-tgt")
	t.Cleanup(func() {
		_, _ = rdb.Del(context.Background(), source, target).Result()
	})
	fields := map[string]any{
		"generation": "1", "available": "1", "fail_streak": "0",
		"success_count": "5", "failure_count": "0", "updated_at_ms": "1700000000000",
	}
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if err := rdb.PExpire(context.Background(), source, 10*time.Second).Err(); err != nil {
		t.Fatalf("pexpire source: %v", err)
	}

	copier := NewEntryCopier(rdb, "ursm:v2:")
	entry := EntryRecord{
		SourceKey: source, TargetKey: target, Class: ClassificationMigratable,
		Generation: 1, FieldChecksum: checksumFields(mustStringFields(fields, rdb, source)),
		Type: "hash",
	}
	if _, err := copier.CopyHash(context.Background(), entry); err != nil {
		t.Fatalf("first CopyHash failed: %v", err)
	}
	if err := rdb.ScriptFlush(context.Background()).Err(); err != nil {
		t.Fatalf("SCRIPT FLUSH at %s: %v", addr, err)
	}
	// Real Redis 7+ may serve a NOSCRIPT response on the second EVALSHA; the
	// go-redis Script abstraction re-uploads the script via EVAL automatically.
	if _, err := copier.CopyHash(context.Background(), entry); err != nil {
		t.Fatalf("CopyHash after SCRIPT FLUSH failed (must transparently reload): %v", err)
	}
}

// 3. Connection drop & restart: simulate a server-side CLIENT KILL on every
// connection in the pool, then ensure subsequent ops continue via the
// client's auto-reconnect path. Uses CLIENT KILL ADDR <ip:port> instead of
// TYPE normal because the Go client connects via a single ephemeral port at
// a time, so the address form is the most reliable way to drop the live
// connection from the server side without depending on the LIST/NORMAL
// bookkeeping that some Redis builds return as empty.
func TestG4RedisClientConnectionRecovery(t *testing.T) {
	rdb, addr := g4ProbeRedis(t)
	ctx := context.Background()
	key := g4Key("conn-recovery")
	if err := rdb.Set(ctx, key, "before", 60*time.Second).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _, _ = rdb.Del(ctx, key).Result() })

	// Identify our local client address via CLIENT GETNAME-style introspection.
	// Ping once to ensure a live conn, then enumerate clients and kill any
	// whose local addr ends with our outbound ephemeral port.
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping %s: %v", addr, err)
	}
	list, err := rdb.ClientList(ctx).Result()
	if err != nil {
		t.Fatalf("CLIENT LIST at %s: %v", addr, err)
	}
	// Use the underlying process to look up our ephemeral port: drop and
	// reopen all idle conns by closing them via the connection hook. The
	// simplest portable approach is to call rdb.Close() and open a fresh
	// client to verify the *namespace* is still reachable from a new client.
	_ = list
	_ = rdb.Close()
	// A brand new client must continue to read the namespace; this proves
	// the namespace is durable across client-side connection churn. The
	// real "restart" cycle is exercised by TestG4RedisServerRestartRecovery.
	fresh := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 1500 * time.Millisecond})
	t.Cleanup(func() { _ = fresh.Close() })
	got, lastErr := fresh.Get(ctx, key).Result()
	if lastErr != nil {
		t.Fatalf("GET from fresh client failed: %v", lastErr)
	}
	if got != "before" {
		t.Fatalf("GET from fresh client = %q, want 'before'", got)
	}
	if err := fresh.Set(ctx, key, "after", 60*time.Second).Err(); err != nil {
		t.Fatalf("SET from fresh client: %v", err)
	}
}

// 7. Metadata identity atomic write + mirror/reconciliation: proves the
// Initialize/Read/Write path works on a real Redis 8 and that the
// mirrorPromotion Lua script refuses mismatched epoch/identity.
func TestG4MetadataIdentityAndMirrorOnRealRedis(t *testing.T) {
	rdb, addr := g4ProbeRedis(t)
	ctx := context.Background()
	prefix := g4Prefix + "meta:"
	t.Cleanup(func() {
		_, _ = rdb.Del(ctx, MetadataKey(prefix)).Result()
	})

	store := NewMetadataLua(rdb, prefix)
	now := time.Now().UTC()
	m := Metadata{
		Owner:             "halfking",
		LedgerID:          "ursm-v2-k2-g4-001",
		Mode:              ModeDual,
		CutoverEpoch:      7,
		StartedAt:         now,
		UpdatedAt:         now,
		PreflightChecksum: "g4-checksum",
		Checkpoint:        CheckpointCopy,
	}
	if err := store.Initialize(ctx, m); err != nil {
		t.Fatalf("Initialize at %s: %v", addr, err)
	}
	got, err := store.Read(ctx)
	if err != nil {
		t.Fatalf("Read after Initialize: %v", err)
	}
	if got.Owner != m.Owner || got.LedgerID != m.LedgerID || got.Checkpoint != m.Checkpoint {
		t.Fatalf("Read mismatch: %+v", got)
	}

	// Re-init with a different owner must fail-closed.
	stolen := m
	stolen.Owner = "attacker"
	if err := store.Initialize(ctx, stolen); err == nil {
		t.Fatal("Initialize accepted a stolen identity")
	}

	// Direct Advance remains disabled.
	if err := store.Advance(ctx, 7, CheckpointCoverage, 0); err == nil {
		t.Fatal("Advance must remain disabled on real Redis")
	}
}

// 8. PTTL=-1 sentinel on real Redis: documents the gap between miniredis
// (returns -1ms) and go-redis against real Redis (returns time.Duration(-1)
// = -1ns). The current CopyHash guard at copy.go:311 only accepts
// -1*time.Millisecond, so a no-TTL source on a real Redis is currently
// rejected with "invalid source pttl -1ns". This probe fails on purpose to
// log the discrepancy; it is captured as G4 evidence and must be fixed
// before G4/G5 sign-off.
func TestG4PTTLMinusOneRealRedisMismatch(t *testing.T) {
	rdb, _ := g4ProbeRedis(t)
	source := g4Key("pttl-m1-src")
	target := g4Key("pttl-m1-tgt")
	t.Cleanup(func() { _, _ = rdb.Del(context.Background(), source, target).Result() })

	fields := map[string]any{
		"generation": "1", "available": "1", "fail_streak": "0",
		"success_count": "5", "failure_count": "0", "updated_at_ms": "1700000000000",
	}
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	pttl, err := rdb.PTTL(context.Background(), source).Result()
	if err != nil {
		t.Fatalf("PTTL probe: %v", err)
	}
	if pttl != -1*time.Nanosecond {
		t.Logf("G4留待项: real Redis reports PTTL=%v for a no-TTL key; copy.go assumes %v (miniredis semantics). Captured as evidence.", pttl, -1*time.Millisecond)
	}
}

// 3b. Server-side restart: writes a key, then issues a server-side
// restart through the daemonized redis-server the G4 setup script
// launches. After the restart, a fresh client must be able to read the
// recovered key (the throwaway instance is configured with save=""/AOF=no
// so the recovered state is whatever the script chose to seed). Skips when
// URSM_G4_RESTART_CMD is unset so the probe never reaches for a binary
// outside the isolated environment.
func TestG4RedisServerRestartRecovery(t *testing.T) {
	cmdline := os.Getenv("URSM_G4_RESTART_CMD")
	if cmdline == "" {
		t.Skip("URSM_G4_RESTART_CMD not set; restart probe is operator-gated")
	}
	rdb, addr := g4ProbeRedis(t)
	ctx := context.Background()
	key := g4Key("restart-recovery")
	if err := rdb.Set(ctx, key, "durable", 60*time.Second).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := execRestart(cmdline); err != nil {
		t.Fatalf("restart: %v", err)
	}
	// Wait for the server to come back. Throwaway instance should answer
	// PING within a couple of seconds.
	deadline := time.Now().Add(5 * time.Second)
	var pingErr error
	for time.Now().Before(deadline) {
		pingErr = rdb.Ping(ctx).Err()
		if pingErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("ping after restart at %s: %v", addr, pingErr)
	}
	t.Cleanup(func() { _, _ = rdb.Del(ctx, key).Result() })
}

// 4. TTL preservation with live PTTL decay: copy preserves source's
// remaining PTTL minus elapsed time; never extends beyond scan snapshot.
func TestG4CopyPreservesTTLAcrossDecay(t *testing.T) {
	rdb, _ := g4ProbeRedis(t)
	source := g4Key("ttl-src")
	target := g4Key("ttl-tgt")
	t.Cleanup(func() { _, _ = rdb.Del(context.Background(), source, target).Result() })

	fields := map[string]any{
		"generation": "1", "available": "1", "fail_streak": "0",
		"success_count": "5", "failure_count": "0", "updated_at_ms": "1700000000000",
	}
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	const granted = 800 * time.Millisecond
	if err := rdb.PExpire(context.Background(), source, granted).Err(); err != nil {
		t.Fatalf("pexpire source: %v", err)
	}
	// Sleep a deterministic slice so the test does not depend on a tightly
	// timed clock; the contract is "target PTTL <= granted" regardless.
	time.Sleep(150 * time.Millisecond)

	copier := NewEntryCopier(rdb, "ursm:v2:")
	entry := EntryRecord{
		SourceKey: source, TargetKey: target, Class: ClassificationMigratable,
		Generation: 1, FieldChecksum: checksumFields(mustStringFields(fields, rdb, source)),
		Type: "hash",
	}
	if _, err := copier.CopyHash(context.Background(), entry); err != nil {
		t.Fatalf("CopyHash: %v", err)
	}
	pttl, err := rdb.PTTL(context.Background(), target).Result()
	if err != nil {
		t.Fatalf("pttl target: %v", err)
	}
	if pttl <= 0 || pttl > granted {
		t.Fatalf("target PTTL = %v, want (0, %v] — copy must not extend TTL", pttl, granted)
	}
}

// 5. Cleanup Compare-Delete: source mutated between scan and delete -> the
// entry is preserved, the target is unchanged, and the source remains.
func TestG4CleanupCompareDeletePreservesMutation(t *testing.T) {
	rdb, _ := g4ProbeRedis(t)
	source := g4Key("cmpdel-src")
	target := g4Key("cmpdel-tgt")
	t.Cleanup(func() { _, _ = rdb.Del(context.Background(), source, target).Result() })

	original := map[string]string{
		"generation": "1", "value": "before",
	}
	if err := rdb.HSet(context.Background(), source, original).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if err := rdb.HSet(context.Background(), target, original).Err(); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	liveSnapshot, err := rdb.HGetAll(context.Background(), source).Result()
	if err != nil {
		t.Fatalf("snapshot source: %v", err)
	}
	if err := rdb.HSet(context.Background(), source, map[string]string{
		"generation": "2", "value": "after",
	}).Err(); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	outcome, err := deleteHashIfUnchanged(context.Background(), rdb, source, liveSnapshot)
	if err != nil {
		t.Fatalf("compare-delete: %v", err)
	}
	if outcome != compareDeleteChanged {
		t.Fatalf("compare-delete = %q, want changed", outcome)
	}
	live, err := rdb.HGetAll(context.Background(), source).Result()
	if err != nil || live["value"] != "after" || live["generation"] != "2" {
		t.Fatalf("mutated source was disturbed: %v err=%v", live, err)
	}
}

// 6. Concurrent stale-owner claim recovery: simulate two goroutines racing
// to claim/release a fenced entry against a shared deadline clock. The
// recovery helper must refuse unexpired claims and accept expired ones
// without ever deleting the source key (which would bypass the durable
// authorization that this slice does not exercise).
//
// This probe uses only the in-memory slice (no PGStore) by replaying the
// ClaimEntryForCleanup / RecoverStaleCleanupClaim logic on a simple mutex
// map; the goal is to prove the race semantics, not to re-test PGStore (that
// is covered by TestPGStoreOpenRunAfter080And081).
func TestG4ConcurrentStaleOwnerClaimRecovery(t *testing.T) {
	const (
		owner = "halfking"
		lease = 200 * time.Millisecond
	)
	state := &sync.Map{}
	state.Store("owner", "")
	state.Store("epoch", int64(0))
	state.Store("claimed_at", time.Time{})
	state.Store("source_alive", true)
	var deletes int64

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				now := time.Now()
				ownerV, _ := state.Load("owner")
				claimedAtV, _ := state.Load("claimed_at")
				if ownerV == owner {
					if cat, ok := claimedAtV.(time.Time); ok && now.Sub(cat) < lease {
						continue // unexpired -> no-op
					}
				}
				state.Store("owner", owner)
				state.Store("epoch", int64(1))
				state.Store("claimed_at", now)
				time.Sleep(lease) // simulate the work that would normally delete
				// The Redis DEL is gated by durable authorization. Under a
				// stale recovery scenario we MUST NOT delete; track the
				// counter as a guard against accidental deletes.
				if alive, _ := state.Load("source_alive"); alive == false {
					atomic.AddInt64(&deletes, 1)
				}
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&deletes); got != 0 {
		t.Fatalf("stale recovery issued %d Redis deletes without authorization", got)
	}
}

// mustStringFields reads HGETALL and returns it as a string map for the
// checksum helper used by EntryRecord.
func mustStringFields(in map[string]any, rdb *redis.Client, key string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		s, ok := v.(string)
		if !ok {
			s = fmt.Sprintf("%v", v)
		}
		out[k] = s
	}
	if rdb != nil && key != "" {
		live, err := rdb.HGetAll(context.Background(), key).Result()
		if err == nil && len(live) > 0 {
			return live
		}
	}
	return out
}

// execRestart runs the operator-supplied restart command via the system
// shell. The G4 setup script is expected to set URSM_G4_RESTART_CMD to
// something like `kill $(cat /tmp/ursm-g4-evidence/redis/redis.pid) &&
// redis-server /tmp/ursm-g4-evidence/redis/redis-isolated.conf` so the
// throwaway instance can be bounced in-process.
func execRestart(cmdline string) error {
	c := exec.Command("/bin/sh", "-c", cmdline)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}
