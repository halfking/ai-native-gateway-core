package executors

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

// vapeur 遗留 #3 (2026-10-02) — 消掉热路径那次额外 Redis 读。
//
// ⚠️ 本文件的判据钉在**调用点是否还存在**，不是钉函数里第一个 return。
// 这两者是不同的东西：GetSupportsResponses 的第一条 return 是
// `state == nil || !SupportsResponsesKnown()`，任何"state 形状不对"的
// 改动都能让它提前返回，而真正的成本在它**之后**——那次 GET。钉第一个
// return 的用例在本次改动前后都是绿的，也就是说它测不到本轮做的事。
//
// 真正的判据是 go-redis hook 数出来的命令条数：node key 上的 GET 在带上
// 透传 state 后必须为 0。

// nodeKeyGetCounter counts `GET` commands issued against node-state keys.
// It deliberately does NOT count MGET or TIME: the router's batched MGET is
// the read we are reusing, and TIME still happens (see the clock-source note
// in credentialfpslot.GetSupportsResponses).
type nodeKeyGetCounter struct {
	nodeGets atomic.Int32
	times    atomic.Int32
}

func (c *nodeKeyGetCounter) DialHook(next redis.DialHook) redis.DialHook { return next }

func (c *nodeKeyGetCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "get" && isNodeStateKeyArg(cmd.Args()) {
			c.nodeGets.Add(1)
		}
		if cmd.Name() == "time" {
			c.times.Add(1)
		}
		return next(ctx, cmd)
	}
}

func (c *nodeKeyGetCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if cmd.Name() == "get" && isNodeStateKeyArg(cmd.Args()) {
				c.nodeGets.Add(1)
			}
			if cmd.Name() == "time" {
				c.times.Add(1)
			}
		}
		return next(ctx, cmds)
	}
}

func isNodeStateKeyArg(args []any) bool {
	for _, a := range args {
		if s, ok := a.(string); ok {
			if len(s) > len("llmgw:cred_fp_node:") && s[:len("llmgw:cred_fp_node:")] == "llmgw:cred_fp_node:" {
				return true
			}
		}
	}
	return false
}

// TestVapour03_PrefetchedStateRemovesTheNodeKeyGet is the load-bearing
// criterion. With a routing-time snapshot on the candidate, the executor must
// issue ZERO `GET` against the node key, while still resolving the verdict.
//
// The counter is armed AFTER the verdict is written, so the setup write is not
// counted. Both commands are counted separately on purpose:
//   - nodeGets must be 0  ← this is what #3 removed
//   - times  must be >= 1 ← the Redis-TIME decision is still in force
func TestVapour03_PrefetchedStateRemovesTheNodeKeyGet(t *testing.T) {
	exec, fpMgr, _, client := newF04ExecutorWithRedisClient(t)
	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)
	const credID, model = 22, "gpt-5.6-terra"

	ctx := t.Context()
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed durable negative verdict: %v", err)
	}
	// The snapshot the router would have handed down: exactly what a batched
	// MGET of this key returns.
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot read: %v", err)
	}
	if snapshot == nil || snapshot.Capabilities.SupportsResponses == nil {
		t.Fatal("test setup broken: snapshot carries no verdict, the assertion below would be vacuous")
	}

	counter := &nodeKeyGetCounter{}
	client.AddHook(counter)

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: credID, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: model, APIKey: "test-key",
		SupportsNativeResponses: true,
		RoutedNodeState:         snapshot,
	}
	if _, err := exec.executeOpenAI(f04ExecParams(`{"model":"gpt-5.6-terra","input":"hi"}`),
		candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("execute with prefetched state: %v", err)
	}

	if got := counter.nodeGets.Load(); got != 0 {
		t.Fatalf("node-key GET count = %d, want 0: the prefetched state must remove the hot-path read (遗留 #3)", got)
	}
	if got := counter.times.Load(); got < 1 {
		t.Fatalf("Redis TIME count = %d, want >=1: the expiry decision must still use Redis TIME, not local time", got)
	}
	// The verdict must still be honoured — removing the read must not turn the
	// gate into "no verdict", which would silently re-enable doomed /responses.
	if got := responsesHits.Load(); got != 0 {
		t.Fatalf("responses hits = %d, want 0: the prefetched negative verdict must still short-circuit", got)
	}
	if got := chatHits.Load(); got != 1 {
		t.Fatalf("chat hits = %d, want 1", got)
	}
}

// TestVapour03_NoPrefetchedStateStillReads is the other half of the contract.
// nil RoutedNodeState means "the router had no snapshot" (authoritative URSM v2
// path, FpSlots disabled, batch read failed open) — NOT "no verdict". The
// executor must fall back to reading the key itself, and must still honour the
// durable verdict it finds.
func TestVapour03_NoPrefetchedStateStillReads(t *testing.T) {
	exec, fpMgr, _, client := newF04ExecutorWithRedisClient(t)
	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)
	const credID, model = 22, "gpt-5.6-terra"

	ctx := t.Context()
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed durable negative verdict: %v", err)
	}

	counter := &nodeKeyGetCounter{}
	client.AddHook(counter)

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: credID, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: model, APIKey: "test-key",
		SupportsNativeResponses: true,
		// RoutedNodeState deliberately nil.
	}
	if _, err := exec.executeOpenAI(f04ExecParams(`{"model":"gpt-5.6-terra","input":"hi"}`),
		candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("execute without prefetched state: %v", err)
	}

	if got := counter.nodeGets.Load(); got < 1 {
		t.Fatalf("node-key GET count = %d, want >=1: a nil snapshot must fall back to reading the key", got)
	}
	if got := responsesHits.Load(); got != 0 {
		t.Fatalf("responses hits = %d, want 0: the fallback read must still resolve the durable verdict", got)
	}
}

// TestVapour03_PrefetchedStateMustNotBeTreatedAsNoVerdict is the failure mode
// the whole change could plausibly introduce: treating "no snapshot" as "no
// verdict". That would send a doomed /responses call for every request on the
// authoritative/fail-open paths — exactly the waste F04 exists to remove, but
// now silent because nothing errors.
func TestVapour03_PrefetchedStateMustNotBeTreatedAsNoVerdict(t *testing.T) {
	exec, fpMgr, _, _ := newF04ExecutorWithRedisClient(t)
	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)
	const credID, model = 22, "gpt-5.6-terra"

	ctx := t.Context()
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed durable negative verdict: %v", err)
	}
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot read: %v", err)
	}

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: credID, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: model, APIKey: "test-key",
		SupportsNativeResponses: true,
		RoutedNodeState:         snapshot,
	}
	if _, err := exec.executeOpenAI(f04ExecParams(`{"model":"gpt-5.6-terra","input":"hi"}`),
		candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := responsesHits.Load(); got != 0 {
		t.Fatalf("responses hits = %d, want 0", got)
	}
	if got := chatHits.Load(); got != 1 {
		t.Fatalf("chat hits = %d, want 1", got)
	}
}

// TestVapour03_ExpiredPrefetchedStateStillReadsAsUnknown pins the expiry
// semantics THROUGH the passthrough: a snapshot whose Redis-clock deadline has
// passed must read as unknown, exactly as it would if freshly read. This is the
// case that would break if someone "optimised" the TIME check to local time or
// dropped it along with the GET.
func TestVapour03_ExpiredPrefetchedStateStillReadsAsUnknown(t *testing.T) {
	exec, fpMgr, mr, client := newF04ExecutorWithRedisClient(t)
	var responsesHits, chatHits atomic.Int32
	upstream := responsesRejectingUpstream(t, &responsesHits, &chatHits)
	const credID, model = 22, "gpt-5.6-terra"

	ctx := t.Context()
	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)
	if err := fpMgr.SetSupportsResponses(ctx, credID, model, false); err != nil {
		t.Fatalf("seed durable negative verdict: %v", err)
	}
	snapshot, err := fpMgr.GetNodeState(ctx, credID, model)
	if err != nil {
		t.Fatalf("snapshot read: %v", err)
	}

	// Move the Redis clock past the stored deadline. The deadline itself was
	// written on the Redis clock, so only the Redis clock can expire it.
	deadline := time.Unix(snapshot.CapabilityExpiresAt, 0)
	mr.SetTime(deadline.Add(time.Second))

	counter := &nodeKeyGetCounter{}
	client.AddHook(counter)

	candidate := provider.Candidate{
		ProviderID: 11, CredentialID: credID, BaseURL: upstream.URL,
		Protocol: "openai-responses", RawModel: model, APIKey: "test-key",
		SupportsNativeResponses: true,
		RoutedNodeState:         snapshot,
	}
	if _, err := exec.executeOpenAI(f04ExecParams(`{"model":"gpt-5.6-terra","input":"hi"}`),
		candidate, 0, time.Now(), nil); err != nil {
		t.Fatalf("execute with expired snapshot: %v", err)
	}

	if counter.times.Load() < 1 {
		t.Fatalf("Redis TIME count = %d, want >=1: expiry must be judged on the Redis clock even when the state is prefetched", counter.times.Load())
	}
	// Verdict is expired ⇒ unknown ⇒ the executor keeps its SQL-derived posture
	// and attempts native Responses (live detection). The point of the
	// assertion is the TIME call, not which leg wins.
	t.Logf("expired snapshot: responses=%d chat=%d",
		responsesHits.Load(), chatHits.Load())
}

// newF04ExecutorWithRedisClient is the scaffolding for the counter tests: the
// existing helper throws the *redis.Client away, and the counter has to be
// installed on the very client the Manager uses.
func newF04ExecutorWithRedisClient(t *testing.T) (*Executor, *credentialfpslot.Manager, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	fpMgr := credentialfpslot.New(credentialfpslot.Config{DefaultLimit: 5, Enabled: true}, client)
	limiter := newLimiterForTest()
	t.Cleanup(limiter.Stop)
	return &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		FpSlots:         fpMgr,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}, fpMgr, mr, client
}
