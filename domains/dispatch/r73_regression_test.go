package dispatch

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// R73 回归：dispatch_governor_snapshot_state 的 {backend, mode, state}
// gauge 不带凭据标签，因此它只能是 (backend, mode) 维度的聚合值。
// 旧实现逐条快照把命中态置 1、其余态置 0，于是「最后一个被遍历到的凭据」
// 决定了全池读数：一个凭据饱和会把其它所有凭据的 ready 清零，而下线的
// 凭据会把 saturated=1 永久留下。RecordSnapshotBatch 改为每 tick 计数聚合，
// 与遍历顺序无关且自清零。
//
// 判别力：把 RecordSnapshotBatch 换回逐条 RecordSnapshot 循环，本测试必须红。
func TestR73_SnapshotStateGaugeAggregatesAcrossCredentials(t *testing.T) {
	const b, m = "local", ModeTPM
	reset := func() {
		for _, s := range stateValues {
			metricGovernorSnapshotState.DeleteLabelValues(b, m, s)
		}
		metricGovernorSnapshotState.DeleteLabelValues("local", ModeRPM, "ready")
	}
	reset()
	t.Cleanup(reset)

	ready := GovernorSnapshot{Backend: b, Mode: m, State: SnapshotStateReady}
	sat := GovernorSnapshot{Backend: b, Mode: m, State: SnapshotStateGovernorSaturated}

	// 凭据 7 ready
	RecordSnapshotBatch([]GovernorSnapshot{ready})
	if got := testutil.ToFloat64(metricGovernorSnapshotState.WithLabelValues(b, m, "ready")); got != 1 {
		t.Fatalf("单凭据 ready 后 ready 应为 1, got %v", got)
	}

	// 本 tick 有 3 个凭据 ready、1 个饱和 —— 无论遍历顺序如何，
	// 聚合读数都必须是 3/1。
	RecordSnapshotBatch([]GovernorSnapshot{sat, ready, ready, ready})
	if got := testutil.ToFloat64(metricGovernorSnapshotState.WithLabelValues(b, m, "ready")); got != 3 {
		t.Errorf("ready 聚合计数 = %v, 期望 3（3 个 ready 凭据）", got)
	}
	if got := testutil.ToFloat64(metricGovernorSnapshotState.WithLabelValues(b, m, "governor_saturated")); got != 1 {
		t.Errorf("governor_saturated 聚合计数 = %v, 期望 1", got)
	}

	// 顺序反转：饱和的排在最后，聚合值必须不变
	RecordSnapshotBatch([]GovernorSnapshot{ready, ready, ready, sat})
	if got := testutil.ToFloat64(metricGovernorSnapshotState.WithLabelValues(b, m, "ready")); got != 3 {
		t.Errorf("遍历顺序反转后 ready = %v, 期望仍为 3（聚合必须与顺序无关）", got)
	}

	// 自清零：下线的饱和凭据不再被观测，下一 tick 饱和计数必须归零。
	RecordSnapshotBatch([]GovernorSnapshot{ready, ready, ready})
	if got := testutil.ToFloat64(metricGovernorSnapshotState.WithLabelValues(b, m, "governor_saturated")); got != 0 {
		t.Errorf("饱和凭据下线后 governor_saturated = %v, 期望 0（必须自清零，不能残留）", got)
	}
	if got := testutil.ToFloat64(metricGovernorSnapshotState.WithLabelValues(b, m, "ready")); got != 3 {
		t.Errorf("ready = %v, 期望 3", got)
	}
}

// R73 回归：observer 捕获 Validate() panic 之后必须丢弃该快照，不能再发布。
// 旧实现 recover 只保 tick 存活，执行流继续走到 collected=append，于是
// State=Ready 且 BackendErr!=nil 的违规快照照样把 ready 置 1 —— 正是
// 「后端故障看起来很健康」的那类误报。
func TestR73_ObserverDropsSnapshotThatViolatesValidateInvariant(t *testing.T) {
	o := &governorSnapshotObserver{
		backend:    func() GovernorBackend { return nil },
		revisionFn: func() uint64 { return 7 },
		provider: staticSnapshotProvider{snaps: []GovernorSnapshot{
			// 违规：State=Ready 但 BackendErr 非 nil → Validate panic
			{Backend: "local", Mode: ModeConcurrency, State: SnapshotStateReady, BackendErr: errBoomR73},
			// 合法：应当被发布
			{Backend: "local", Mode: ModeConcurrency, State: SnapshotStateReady},
		}},
	}
	for _, s := range stateValues {
		metricGovernorSnapshotState.DeleteLabelValues("local", ModeConcurrency, s)
	}
	t.Cleanup(func() {
		for _, s := range stateValues {
			metricGovernorSnapshotState.DeleteLabelValues("local", ModeConcurrency, s)
		}
	})

	o.tick()

	// 只有 1 个合法快照参与聚合 → ready 必须是 1；若违规快照也被发布，
	// 旧实现的逐条循环同样得到 1，因此这里断言聚合计数路径的丢弃语义：
	// 用两个合法 ready + 一个违规快照，期望 ready=2 而非 3。
	o2 := &governorSnapshotObserver{
		backend:    func() GovernorBackend { return nil },
		revisionFn: func() uint64 { return 7 },
		provider: staticSnapshotProvider{snaps: []GovernorSnapshot{
			{Backend: "local", Mode: ModeConcurrency, State: SnapshotStateReady, BackendErr: errBoomR73},
			{Backend: "local", Mode: ModeConcurrency, State: SnapshotStateReady},
			{Backend: "local", Mode: ModeConcurrency, State: SnapshotStateReady},
		}},
	}
	o2.tick()
	if got := testutil.ToFloat64(metricGovernorSnapshotState.WithLabelValues("local", ModeConcurrency, "ready")); got != 2 {
		t.Errorf("ready 聚合计数 = %v, 期望 2（违反 Validate 不变量的快照必须被丢弃）", got)
	}
}

// R73 回归：DimensionIndex.Sweep 必须全环扫描。旧实现只扫环首前缀，
// 遇到第一个未过期条目即 break；而 ExpiresAt 由 Complete/UpdateWait 原地
// 刷新、环序为插入序，因此一个被 Track 但从未走到终态的请求（丢弃/取消，
// ExpiresAt 为零值）会永远钉在环首，让其后所有已过期条目永不回收 ——
// 整个维度的 TTL 契约失效。
func TestR73_SweepRecoversPastAnUnterminatedHeadEntry(t *testing.T) {
	cfg := DefaultDimensionIndexConfig()
	cfg.TTL = 20 * time.Millisecond
	cfg.PerKeyCapacity = 128
	cfg.MaxKeys = 64
	ix := NewDimensionIndex(cfg)

	// A：被 Track 但永不 Complete/UpdateWait → ExpiresAt 保持零值，钉在环首
	ix.Track(&QueuedRequest{ID: "r73-A", RequestedModel: "gpt-4o"}, time.Now())

	// B：很久之前就已完成，早该被 TTL 回收
	old := time.Now().Add(-time.Hour)
	qrB := &QueuedRequest{ID: "r73-B", RequestedModel: "gpt-4o"}
	ix.Track(qrB, old)
	ix.Complete(qrB, ForwardOutcome{}, old)

	key := dimensionKey(DimensionModel, "gpt-4o")
	if ring := ix.rings[key]; ring == nil || len(ring.entries) < 2 {
		t.Fatalf("前置条件不成立：环内应至少有 A、B 两条, got %v", ix.rings[key])
	}

	evicted := ix.Sweep(time.Now())
	if evicted < 1 {
		t.Fatalf("Sweep 回收 %d 条，期望至少 1 条（过期的 B 必须被回收，不能被环首 A 挡住）", evicted)
	}
	ring := ix.rings[key]
	if ring == nil {
		t.Fatalf("Sweep 回收后环不应消失（B 仍应保留? 不，A 未终态应保留）")
	}
	for _, e := range ring.entries {
		if e.RequestID == "r73-B" {
			t.Errorf("已过期的 B 仍在环内：Sweep 的环首 break 让它逃过了 TTL 回收")
		}
	}
}

var errBoomR73 = errR73Boom{}

type errR73Boom struct{}

func (errR73Boom) Error() string { return "r73 injected backend failure" }

// staticSnapshotProvider 是测试用的 SnapshotProvider 桩。
type staticSnapshotProvider struct {
	snaps []GovernorSnapshot
}

func (s staticSnapshotProvider) ForEachCredSnapshot(fn func(GovernorSnapshot) error) error {
	for _, snap := range s.snaps {
		if err := fn(snap); err != nil {
			return err
		}
	}
	return nil
}

func (s staticSnapshotProvider) ActiveRevision() uint64 { return 7 }

func (s staticSnapshotProvider) SnapshotForCred(int) (SnapshotState, bool) {
	return SnapshotStateUnknown, false
}

// R73 回归（P0）：模型泳道触顶时请求必须以显式拒绝结束，不能返回 (nil, nil)。
// 旧实现在 drainTotalOne 里把「泳道不可用」也翻译成 ctxOf(qr).Err()，ctx 存活时
// 它是 nil，于是 Submit 返回 (nil, nil) —— 一个从未到达任何 provider 的请求被
// 当成成功，terminalActionOf 还会往 journal 里写 NextActionCompleted。
//
// 判别力：把修复退回 ctxOf(qr).Err() 一行，本测试必须红。
func TestR73_ModelLaneCapRejectsInsteadOfReportingSuccess(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"a": {cred(1, ModeConcurrency, 5)}, "b": {cred(2, ModeConcurrency, 5)}},
		forwardFn:    func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		forwardCalls: map[int]int{},
	}
	cfg := DefaultConfig()
	cfg.MaxModelLanes = 1
	// 关闭空闲回收，让泳道数只增不减，稳定复现触顶
	cfg.ModelLaneIdleSeconds = 0
	hot := &atomic.Value{}
	hot.Store(&cfg)

	p := NewPipeline(Deps{
		RouteFunc:        f.routeFunc,
		ModelResolveFunc: f.modelResolveFunc,
		ForwardFunc:      f.forwardFunc,
		HotCfg:           hot,
	})
	p.Start()
	defer p.Stop()

	// 第一个请求占住唯一的模型泳道
	first := NewQueuedRequest("r73-lane-a", "t", "a", context.Background(), "payload")
	if _, err := p.Submit(context.Background(), first); err != nil {
		t.Fatalf("第一个请求应成功, err=%v", err)
	}

	// 第二个模型需要新泳道但已达上限
	second := NewQueuedRequest("r73-lane-b", "t", "b", context.Background(), "payload")
	res, err := p.Submit(context.Background(), second)

	if err == nil {
		t.Fatalf("泳道触顶时必须返回错误，却得到 err=nil res=%v —— 未路由的请求被报成了成功", res)
	}
	if res != nil {
		t.Errorf("失败路径不应带结果, res=%v", res)
	}
	var ov *OverflowError
	if !errors.As(err, &ov) {
		t.Errorf("期望 OverflowError（与其它背压路径同口径）, got %T: %v", err, err)
	}
	f.mu.Lock()
	calls2 := f.forwardCalls[2]
	f.mu.Unlock()
	if calls2 != 0 {
		t.Errorf("凭据 2 被调用了 %d 次，被拒的请求不应发出任何上游调用", calls2)
	}
}

// R73 回归（P2 #9）：Tier-0 溢出拒绝必须经 complete() 收终态。旧实现在
// Submit 的 total_queue_full 拒绝分支里手搓 registry.MarkCompleted +
// emitRequestTerminal 并绕过 complete()，而请求在拒绝前已经被
// dimensionIndex.Track() —— 分维条目永远停在 pending、ExpiresAt 为零值
// （全环 Sweep 刻意保留零值条目，不会兜底回收这条幽灵）。
//
// 判别力：把 complete() 退回手搓两行（MarkCompleted + emitRequestTerminal），
// 本测试必须红（条目停在 pending、ExpiresAt 零值）。
func TestR73_TotalQueueOverflowRejectCompletesDimensionEntry(t *testing.T) {
	cfg := DefaultConfig()
	// DispatcherWorkers=0：dispatchIn 无人消费 → 模型 drainer 卡死在
	// dispatchIn hand-off（与 TestPipelineClusterQueueBackendIntegration
	// 同一阻塞手法，纯进程内、无 Redis）。
	cfg.DispatcherWorkers = 0
	cfg.MaxQueueDepth = 1
	cfg.TotalQueueCapacity = 1
	hot := &atomic.Value{}
	hot.Store(&cfg)

	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 5)}},
		forwardFn:    func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		forwardCalls: map[int]int{},
	}
	p := NewPipeline(Deps{
		RouteFunc:        f.routeFunc,
		ModelResolveFunc: f.modelResolveFunc,
		ForwardFunc:      f.forwardFunc,
		HotCfg:           hot,
	})
	p.Start()
	defer p.Stop()

	submitAsync := func(id string) context.CancelFunc {
		ctx, cancel := context.WithCancel(context.Background())
		qr := NewQueuedRequest(id, "t", "m", ctx, nil)
		go func() { _, _ = p.Submit(ctx, qr) }()
		return cancel
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("状态未到达（%s）", what)
	}
	modelLaneDepth := func(name string) int64 {
		p.modelMu.Lock()
		mq, ok := p.models[name]
		p.modelMu.Unlock()
		if !ok {
			return -1 // lane 尚未创建
		}
		mq.mu.Lock()
		defer mq.mu.Unlock()
		return mq.depth.Load()
	}

	// 铺满链路：q1 卡住模型 drainer（dispatchIn 无消费者），q2 占满唯一
	// lane 槽位，q3 被 total drainer 弹出后卡进 lane 背压循环（占住
	// total drainer），q4 停进 Tier-0 FIFO 的唯一槽位。
	c1 := submitAsync("r73-tqf-q1")
	defer c1()
	waitFor("q1 停在卡死的模型 drainer 上", func() bool {
		return p.totalQueue.depth() == 0 && modelLaneDepth("m") == 0
	})
	c2 := submitAsync("r73-tqf-q2")
	defer c2()
	waitFor("q2 停在模型 lane", func() bool {
		return p.totalQueue.depth() == 0 && modelLaneDepth("m") == 1
	})
	c3 := submitAsync("r73-tqf-q3")
	defer c3()
	// 「被 total drainer 吸收」= drainer 已经把 q3 从 channel 里取走、并且
	// 因为 lane 满而卡在 enqueueModelFromTotalLane 的背压循环里（还没
	// releaseTotal）。
	//
	// 旧判据是 `depth() == 0`，方向是反的：submitAsync 只是拉起一个
	// goroutine，q3 尚未入队时 depth 本来就等于 0（q1/q2 已 release），
	// 于是这个 wait 立刻返回、什么都没验证；一旦 q3 真的跑起来入队，
	// depth 变成 1 并**永久**停在 1（drainer 卡住 ⇒ 永不 releaseTotal），
	// 判据反而永远不成立 → 满负载下必挂在 5s 超时。也就是说这条门是
	// 「q3 没启动时绿、q3 真启动了才红」。
	//
	// depth 计的是 queued（入队 +1、releaseTotal 才 -1），channel 长度计的
	// 是「还在缓冲里没被取走」。drainer 取走但没 release ⇒ len==0 && depth==1；
	// 这个组合既排除了「还没入队」，也排除了「已 release」，才是真的在断言
	// 「drainer 接手了 q3 并被它占住」。
	waitFor("q3 被 total drainer 吸收", func() bool {
		return len(p.totalQueue.ch) == 0 && p.totalQueue.depth() == 1
	})

	// q4：Tier-0 的唯一槽位（TotalQueueCapacity=1）此刻仍被卡在 drainer
	// 里的 q3 占着（它没 release），所以 q4 走的是溢出拒绝，而不是「停进
	// FIFO」。
	//
	// 旧写法把 q4 异步提交后等 `depth() == 1` —— 该条件被 q3 顶着一个恒真，
	// wait 立即返回：既没验证 q4，又让 q4 的溢出计数与紧随其后的
	// DeleteLabelValues 竞态（q4 先跑⇒计数 1 被清掉、q5 补成 1；q4 后跑⇒
	// 清完再叠加成 2，下面「恰好一次」就红）。改为同步提交并直接断言拒绝，
	// 竞态随之消失，且把「q4 也被拒」这个真实行为写进测试。
	q4 := NewQueuedRequest("r73-tqf-q4", "t", "m", context.Background(), nil)
	if res4, err4 := p.Submit(context.Background(), q4); res4 != nil || err4 == nil {
		t.Fatalf("Tier-0 槽位被 q3 占满时 q4 必须显式拒绝, got res=%v err=%v", res4, err4)
	}

	metricOverflow.DeleteLabelValues("total_queue_full")

	// q5：Tier-0 已满 → Submit 的溢出拒绝路径。
	q5 := NewQueuedRequest("r73-tqf-q5", "t", "m", context.Background(), nil)
	res, err := p.Submit(context.Background(), q5)
	if res != nil || err == nil {
		t.Fatalf("Tier-0 满时必须显式拒绝, got res=%v err=%v", res, err)
	}
	var overflow *OverflowError
	if !errors.As(err, &overflow) || overflow.Reason != "total_queue_full" {
		t.Fatalf("期望 OverflowError(total_queue_full), got %T: %v", err, err)
	}
	// 溢出计数恰好一次：补调 complete() 不得双计（complete 路径不再触碰
	// metricOverflow / observeOverflow）。
	if got := testutil.ToFloat64(metricOverflow.WithLabelValues("total_queue_full")); got != 1 {
		t.Errorf("metricOverflow{total_queue_full} = %v, 期望恰好 1（拒绝必须单次计数）", got)
	}
	// 幽灵断言（缺陷本体）：拒绝请求的分维条目必须已终态且带 TTL 时间戳。
	entries, ok := p.dimensionIndex.EntriesByRequest(q5.ID)
	if !ok || len(entries) == 0 {
		t.Fatalf("被拒请求的分维条目应存在（Track 先于拒绝）, ok=%v", ok)
	}
	for _, e := range entries {
		if e.State != DimensionStateCompleted {
			t.Errorf("分维条目 state = %q, 期望 completed —— pending 幽灵正是本缺陷本体", e.State)
		}
		if e.ExpiresAt.IsZero() {
			t.Errorf("分维条目 ExpiresAt 为零值 —— 没有 TTL 时间戳，条目永远不会老化")
		}
		if e.Outcome != "failure" {
			t.Errorf("分维条目 outcome = %q, 期望 failure", e.Outcome)
		}
	}
	// journal 必须以终态收尾（complete() 的 CAS 内写入，恰好一次）。
	snap := q5.JournalSnapshot()
	if len(snap) == 0 || !isTerminalAction(snap[len(snap)-1].Action) {
		t.Fatalf("被拒请求的 journal 必须以终态动作收尾, got %+v", snap)
	}
}

// R73 回归（P1）：circuit_open / fp_slot_saturated 是网关侧准入信号，
// errorsx 把它们定为终态，planner 的白名单曾漏项并把它们改写成
// KindTransient → ActionRetrySameNode（对已熔断的凭据反复重试）。
func TestR73_CircuitOpenAndFpSlotAreNotSameNodeRetryable(t *testing.T) {
	for _, kind := range []string{
		string(errorsx.KindCircuitOpen),
		string(errorsx.KindFpSlotSaturated),
	} {
		if !isCentralPolicyKind(errorsx.ErrorKind(kind)) {
			t.Errorf("%s 必须被 planner 视为 central（终态）kind，否则会被降级为可重试的 transient", kind)
		}
	}

	// 端到端：带 circuit_open 的失败不得产出 retry_same_cred
	qr := NewQueuedRequest("r73-planner", "t", "m", context.Background(), "payload")
	d := PlanAfterFailure(qr, ForwardOutcome{
		Err:       errors.New("dispatch: circuit open"),
		ErrorKind: string(errorsx.KindCircuitOpen),
	}, DefaultConfig())
	if d.Action == NextActionRetrySameCred {
		t.Errorf("circuit_open 的动作 = %v（%s），期望切走而非同节点重试", d.Action, d.Reason)
	}
}
