// node_state_time_piggyback_test.go — TIME 搭车的**可行性判据**（2026-10-03）。
//
// 背景：遗留 #3 的最后一步是把热路径的最后一次 Redis 往返也省掉——
// 让路由的批量读同时返回 TIME 与 states（Lua 一次返回两者），热路径
// 2 次往返 → 0 次。
//
// ⚠️ handoff 明确要求：「须先想清楚：它会与 §10.3 的『TIME 必须在 state
// 之后采样』冲突吗？」且纪律是「**要用判据证明，不要靠推理**」。
// 本文件就是那个判据的答案，**先于实现**。
//
// 为什么这是个真问题而不是形式问题：
// 现有承重性质（TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch）要求
// TIME 在 state 读**之后**采样，这样「state 读到手之后、判定之前」跨过的
// 到期能被看见。搭车把 TIME 挪到 state **同一次**脚本里——那 TIME 与 state
// 来自同一个原子快照，两者之间**没有缝隙**，跨过的到期**看不见**。
//
// 所以本文件先钉住「搭车方案必须仍然能看见跨过的到期」这个性质，
// 并把当前实现（TIME 单独采样、state 之后）作为它的对照组。
//
// ══ 结论（2026-10-03，由判据得出，不是推理）══
//
// 用变异 P1 实测「把 TIME 采样提到 state 之前」（= 搭车的语义）：
//
//	TestF04_CapabilityReadRechecksRedisTimeAfterStateFetch   红（既有门）
//	TestPiggyback_OrderingPropertyMustSurviveAnyTimeSamplingStrategy  红（本轮新门）
//	TestPiggyback_FutureDeadlineIsStillHonoured              绿（对照，必须绿）
//	TestPiggyback_RoundTripInventory                         绿（只记成本）
//
// ⇒ **搭车（TIME 与 states 同一次脚本返回）与 §10.3 的承重性质直接冲突。**
// 原因是结构性的，不是实现细节：现有 deadline 是**绝对时间戳**
// （`capability_expires_at`，写在 Redis 时钟上），判定要拿它与「读到手之后」
// 的时刻比。搭车让 TIME 与 state 来自同一个原子快照 ⇒ 两者之间没有缝隙 ⇒
// 「读到之后、判定之前」跨过的到期**结构性地看不见**。而请求在路由 MGET
// 之后还要在 dispatch 队列里等（实测 P99 ≈ 17.9s，见
// docs/audit/2026-10-03-prefetch-age-calibration.md），那段等待正是要防的。
//
// ⇒ 本轮**不做**搭车。留下的判据把该性质钉在两层（既有 F04 门 + 本轮
// 新门），使将来任何人再动采样顺序时**立刻判红**，而不是靠读注释自觉。
//
// ⚠️ 若将来仍要做（例如改成「TIME 在 state 之后单独采」以外的新形态），
// 必须先回答一个问题：**判定所用的时刻从哪里来，且它是否 ≥ state 读到的
// 时刻？** 答不上来就不能做。
package credentialfpslot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// advanceClockAfterStateRead 在 state 读到手之后把 miniredis 的时钟拨到 at。
//
// ⚠️ 这是本文件的关键形状，踩过一次：判据若在**调用之前**就把时钟拨好，
// 那么无论 TIME 是在 state 之前还是之后采样，看到的都是同一个「已推进」
// 的时钟 ⇒ 两种实现都会判绿。真实的危险是「读 state 与判定之间」那段时间
// 跨过 deadline，所以时钟必须在**两条命令之间**推进。
type advanceClockAfterStateRead struct {
	server   *miniredis.Miniredis
	at       time.Time
	advanced bool
}

func (h *advanceClockAfterStateRead) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *advanceClockAfterStateRead) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err == nil && cmd.Name() == "get" && !h.advanced {
			h.server.SetTime(h.at)
			h.advanced = true
		}
		return err
	}
}

func (h *advanceClockAfterStateRead) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		return next(ctx, cmds)
	}
}

// TestPiggyback_OrderingPropertyMustSurviveAnyTimeSamplingStrategy states the
// property that any TIME-sampling strategy — including a piggybacked one —
// must satisfy:
//
//	若 deadline 在「state 读到手」与「判定」之间跨过，判定必须看到它已过期。
//
// 这是 §10.3 的承重性质，与「TIME 从哪来」无关：只要判定所用的时刻
// **不早于** state 被读到的时刻，性质就成立。
//
// 形状（承重）：时钟在 **GET 之后、TIME/判定之前** 推进——用 hook 卡在两条
// Redis 命令之间，而不是在调用前预设。第一版就是在调用前预设的，结果
// 「TIME 提前到 state 之前」这个变异**也判绿**，差点让搭车方案带着一个
// 假判据过关。
func TestPiggyback_OrderingPropertyMustSurviveAnyTimeSamplingStrategy(t *testing.T) {
	mgr, mr := newCapabilityTestManager(t)
	ctx := context.Background()
	const credentialID = 77
	const model = "gpt-5.6-terra"
	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)

	require.NoError(t, mgr.SetSupportsResponses(ctx, credentialID, model, true))
	state, err := mgr.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)
	require.NotNil(t, state)
	// deadline 落在 base+1s：GET 之后推进到 base+2s 即跨过它。
	state.CapabilityExpiresAt = base.Add(time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))

	hook := &advanceClockAfterStateRead{server: mr, at: base.Add(2 * time.Second)}
	mgr.client.AddHook(hook)

	supported, known, err := mgr.GetSupportsResponses(ctx, credentialID, model, nil)
	require.NoError(t, err)
	assert.True(t, hook.advanced, "判据前提失败：必须在 GET 之后推进时钟")
	assert.False(t, known,
		"deadline 在读到 state 之后、判定之前跨过，必须判为 unknown。"+
			"任何把 TIME 提前到 state 之前的方案都会在这里判红——"+
			"那就是 §10.3 的「TIME 必须在 state 之后采样」被破坏。")
	assert.False(t, supported)
}

// TestPiggyback_FutureDeadlineIsStillHonoured 是对照组：性质不能被写成
// 「一律判过期」或「一律采信」。deadline 明确在未来时必须仍然可用。
//
// ⚠️ 这条是给「把 TIME 提前」这个改动的**反向**变异用的：若有人为了省往返
// 把 TIME 挪到 state 之前并顺手把比较方向弄反，这条会判红。
func TestPiggyback_FutureDeadlineIsStillHonoured(t *testing.T) {
	mgr, mr := newCapabilityTestManager(t)
	ctx := context.Background()
	const credentialID = 78
	const model = "gpt-5.6-terra"
	base := time.Unix(1_800_000_000, 0)
	mr.SetTime(base)

	require.NoError(t, mgr.SetSupportsResponses(ctx, credentialID, model, true))
	state, err := mgr.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)
	state.CapabilityExpiresAt = base.Add(time.Hour).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))
	// 同样把时钟拨到 GET 之后，deadline 仍远在未来 ⇒ 必须仍然可用。
	hook := &advanceClockAfterStateRead{server: mr, at: base.Add(2 * time.Second)}
	mgr.client.AddHook(hook)

	supported, known, err := mgr.GetSupportsResponses(ctx, credentialID, model, nil)
	require.NoError(t, err)
	assert.True(t, hook.advanced, "判据前提失败：必须在 GET 之后推进时钟")
	assert.True(t, known, "deadline 明确在未来，必须仍然可用——"+
		"否则「一律判过期」也能骗过上一条")
	assert.True(t, supported)
}

// TestPiggyback_RoundTripInventory measures what a piggyback would actually
// have to save, so the decision is made against a number rather than a hope.
//
// 当前实现的账：路由 MGET 之后，闸门内 1 次 TIME（复用快照时 GET=0）。
// 搭车若成立 ⇒ 0 次；不成立则维持 1 次。
//
// ⚠️ 本例不假设搭车一定安全——它只如实记录当前成本。是否值得改由
// 上面两条性质判据决定。
func TestPiggyback_RoundTripInventory(t *testing.T) {
	mgr, mr := newCapabilityTestManager(t)
	ctx := context.Background()
	const credentialID = 79
	const model = "gpt-5.6-terra"
	mr.SetTime(time.Unix(1_800_000_000, 0))
	require.NoError(t, mgr.SetSupportsResponses(ctx, credentialID, model, false))
	snapshot, err := mgr.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)

	counter := &timeCmdCounter{}
	mgr.client.AddHook(counter)
	_, _, err = mgr.GetSupportsResponses(ctx, credentialID, model, snapshot)
	require.NoError(t, err)

	times := counter.get("time")
	gets := counter.get("get")
	t.Logf("透传命中：闸门内 GET=%d TIME=%d；热路径合计（加路由 MGET）=%d 次往返",
		gets, times, gets+times+1)
	// 当前实现的承重值：复用快照时闸门内不得有 GET，TIME 恰好 1 次。
	assert.Equal(t, 0, gets, "复用快照时闸门内不应有 GET")
	assert.Equal(t, 1, times,
		"当前实现 TIME 恰好 1 次。搭车若要成立，它必须降到 0；"+
			"若升到 2，说明多采了一次——那就是把省下的往返又还回去了。")
}

type timeCmdCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *timeCmdCounter) DialHook(next redis.DialHook) redis.DialHook { return next }

func (c *timeCmdCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		c.bump(cmd.Name())
		return next(ctx, cmd)
	}
}

func (c *timeCmdCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			c.bump(cmd.Name())
		}
		return next(ctx, cmds)
	}
}

func (c *timeCmdCounter) bump(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	c.counts[name]++
}

func (c *timeCmdCounter) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}
