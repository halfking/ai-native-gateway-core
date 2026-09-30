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
