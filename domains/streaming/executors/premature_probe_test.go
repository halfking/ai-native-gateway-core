package executors

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
)

// TestProbe_AgeGuardComparesTheWrongClock is a PREMATURE probe.
//
// The existing age-guard suite (capability_passthrough_audit_test.go) always
// writes the verdict and reads the snapshot within the SAME simulated second,
// or advances the clock by exactly the guard window. Every one of those
// scenarios makes "verdict write time" and "snapshot read time" coincide.
//
// In production they are different quantities:
//
//	capability_updated_at = when the VERDICT was written (set by
//	                        setNodeCapabilityScript, TTL 3600s)
//	snapshot age         = when the ROUTER MGET happened (a few ms before the
//	                        gate, plus unbounded dispatch queue wait)
//
// A verdict is written by a background probe on a ~6h cadence and refreshed by
// real traffic. So a snapshot read right now normally carries a
// capability_updated_at that is MINUTES old, while the snapshot itself is ~6ms
// old. The guard `now - CapabilityUpdatedAt > 5` therefore fires on nearly
// every request, re-reading the key it was just handed.
//
// The gate is written so that "trusted the snapshot" and "re-read from Redis"
// produce DIFFERENT observable results: after taking the snapshot we blank the
// verdict on the server. Trusting the snapshot returns (true, true);
// re-reading returns (false, false). The seed is `true` precisely so the two
// branches differ in BOTH return values — with a `false` seed they would
// differ only in `known`, which is a weaker signal.
func TestProbe_AgeGuardComparesTheWrongClock(t *testing.T) {
	_, fpMgr, mr, _ := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)
	// A background probe wrote this verdict 30 MINUTES ago. Well within the
	// 3600s TTL, so it is a perfectly live verdict.
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mr.SetTime(base.Add(30 * time.Minute))

	// The router MGETs NOW, 6ms later the gate runs. The snapshot is 6ms old.
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	mr.SetTime(base.Add(30*time.Minute + 6*time.Millisecond))

	verdictWrittenAt := time.Unix(snapshot.CapabilityUpdatedAt, 0)
	t.Logf("verdict 写入于 %v 前；快照本身只有 6ms 新（dispatch 队列常见间隔）",
		base.Add(30*time.Minute).Sub(verdictWrittenAt))

	// Blank the verdict server-side so "trusted" and "re-read" differ.
	blanked, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	blanked.Capabilities.SupportsResponses = nil
	blanked.CapabilityExpiresAt = 0
	if err := fpMgr.SetNodeState(ctx, blanked); err != nil {
		t.Fatalf("clear verdict: %v", err)
	}

	supported, known, err := fpMgr.GetSupportsResponses(ctx, credID, model, snapshot)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !known || !supported {
		t.Fatalf("(supported=%v, known=%v), want (true, true): a 6ms-old snapshot carrying a "+
			"30-minutes-old but live verdict MUST be trusted. Rejecting it means the guard is "+
			"comparing now against capability_updated_at (verdict WRITE time, TTL 3600s) "+
			"instead of the snapshot's READ time, so it fires on nearly every production "+
			"request and the round-trip saving #3 exists for is given back on the hot path.",
			supported, known)
	}
}

// TestProbe_StaleSnapshotMustNotReReadOnTheHotPath is the decisive
// measurement: the same production shape, counted.
//
// Before this change the gate cost MGET + GET + TIME = 3 round trips.
// With reuse working it is MGET + TIME = 2.
// With the snapshot being rejected it was MGET + TIME + GET + TIME = 4 —
// i.e. the "optimisation" was one round trip WORSE than the code it replaced.
//
// This is the criterion shape the previous round asked for and did not have:
// it answers both "what did we save" and "what did we spend", on the path that
// actually occurs in production rather than on a same-second fixture.
func TestProbe_StaleSnapshotMustNotReReadOnTheHotPath(t *testing.T) {
	_, fpMgr, mr, client := newF04ExecutorWithRedisClient(t)
	const credID, model = 22, "gpt-5.6-terra"
	ctx := t.Context()

	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mr.SetTime(base.Add(30 * time.Minute))
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	counter := &redisCmdCounter{}
	client.AddHook(counter)

	if _, _, err := fpMgr.GetSupportsResponses(ctx, credID, model, snapshot); err != nil {
		t.Fatalf("read: %v", err)
	}

	gets := counter.get("get")
	times := counter.get("time")
	t.Logf("30 分钟前写的 verdict + 6ms 前的快照：GET=%d TIME=%d（闸门内合计 %d 次往返；"+
		"加上路由 MGET 则热路径共 %d 次）", gets, times, gets+times, gets+times+1)
	if gets != 0 {
		t.Fatalf("GET=%d, want 0：一张 6ms 新的快照被丢弃并重读，热路径比改动前多一次往返。"+
			"根因是护栏拿 now 去比 capability_updated_at（verdict 写入时刻，TTL 3600s），"+
			"而应该比的是快照的读取时刻。", gets)
	}
	if times != 1 {
		t.Fatalf("TIME=%d, want exactly 1：闸门内只应采一次 Redis TIME（期限检查）。"+
			"年龄是本地时长，不得再采样 Redis 时钟。", times)
	}
}

// TestProbe_ZeroValuedSnapshotIsUnstamped documents the second reason label and
// pins that it is reachable from the real batch path (a key that does not
// exist yields a zero-value state, not nil).
func TestProbe_ZeroValuedSnapshotIsUnstamped(t *testing.T) {
	_, fpMgr, _, _ := newF04ExecutorWithRedisClient(t)
	ctx := t.Context()

	states, err := fpMgr.GetNodeStatesBatch(ctx, []credentialfpslot.NodeStateKey{
		{CredentialID: 999, Model: "gpt-5.6-terra"},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(states) != 1 || states[0] == nil {
		t.Fatalf("batch returned %d states, first nil=%v", len(states), len(states) > 0 && states[0] == nil)
	}
	// A missing key must come back UNSTAMPED, so the "unstamped" drop reason is
	// a real code path and not a theoretical label.
	if states[0].CapabilityUpdatedAt != 0 {
		t.Fatalf("premise broken: missing key yielded CapabilityUpdatedAt=%d, want 0",
			states[0].CapabilityUpdatedAt)
	}
	if states[0].Capabilities.SupportsResponsesKnown() {
		t.Fatal("premise broken: zero-valued state claims a known verdict")
	}
}
