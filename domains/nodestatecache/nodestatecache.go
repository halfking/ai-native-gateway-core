package nodestatecache

import (
	"context"
	"sync"
	"time"
)

// DefaultCapacity 注册表默认容量（dense id 上界，含 0 号保留位）。
const DefaultCapacity int32 = 65536

// Options 构造选项（全部含默认值）。Clock 可注入（fake clock 便于测试）。
type Options struct {
	// Capacity 节点注册表容量上界（默认 65536）；满时 LRU 回收未活跃 id。
	Capacity int32
	// FailStreakLimit 连续失败阈值（默认 3，对齐 URSM FailStreakLimit）。
	FailStreakLimit uint8
	// BackoffBase / BackoffCap NeedProbe 退避基准与封顶（默认 30s / 30m）。
	BackoffBase time.Duration
	BackoffCap  time.Duration
	// RecentSuccessWindow 36h 成功回看窗口（默认 36h）。
	RecentSuccessWindow time.Duration
	// ResetPeriod 分钟窗重置 ticker 周期（默认 1m；<=0 不启动，仅手动 ResetWindow）。
	ResetPeriod time.Duration
	// ReconcilePeriod 对账 ticker 周期（默认 30s；<=0 不启动，仅手动 Reconcile）。
	ReconcilePeriod time.Duration
	// StartTickers 是否随 New 启动内置 ticker（默认 true；测试可关）。
	StartTickers bool
	// Clock 时钟注入（默认 time.Now）。
	Clock func() time.Time

	// —— 注入接口（五个集成接线点）——
	Scorer        Scorer                // 幸存集评分排序（URSM FilterAndScoreReadyWithSource）
	StickyLookup  StickyLookup          // 同会话 sticky 绑定查询
	ModelFallback ModelFallback         // 任务类型→品质档位换模型
	RecentSuccess RecentSuccessLookup   // 36h 成功回看（NeedProbe）
	Authority     AuthoritativeSnapshot // 权威快照（周期对账）
	// Invalidation 置脏/回收监听（pub/sub 失效广播接入点）。
	Invalidation InvalidationListener
}

func (o *Options) fillDefaults() {
	if o.Capacity <= 0 {
		o.Capacity = DefaultCapacity
	}
	if o.FailStreakLimit == 0 {
		o.FailStreakLimit = 3 // URSM manager.go FailStreakLimit
	}
	if o.BackoffBase <= 0 {
		o.BackoffBase = 30 * time.Second
	}
	if o.BackoffCap <= 0 {
		o.BackoffCap = 30 * time.Minute
	}
	if o.RecentSuccessWindow <= 0 {
		o.RecentSuccessWindow = 36 * time.Hour
	}
	if o.ResetPeriod == 0 {
		o.ResetPeriod = time.Minute
	}
	if o.ReconcilePeriod == 0 {
		o.ReconcilePeriod = 30 * time.Second
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
}

// Cache 是节点状态缓存本体。New 一次返回三闭包（Selector/Updater/NeedProbe）
// 与资源仪表组（TryAcquire/Release/ResetWindow）+ 对账（Reconcile）。
// 三闭包单一责任、互不调用；共享本结构内的位图/统计/资源表。
type Cache struct {
	opts          Options
	reg           *registry
	bitmap        *bitmapTable
	stats         *statsTable
	res           *resourceTable
	scorer        Scorer
	sticky        StickyLookup
	fallback      ModelFallback
	recentSuccess RecentSuccessLookup

	stopOnce sync.Once
	stop     chan struct{}
}

// New 构造节点状态缓存。StartTickers（默认 true）时启动两个内置 ticker：
// 分钟窗重置（ResetPeriod，默认 1m）与对账（ReconcilePeriod，默认 30s，
// 需注入 Authority）。Close 停止。
func New(opts Options) *Cache {
	opts.fillDefaults()
	c := &Cache{
		opts:          opts,
		scorer:        opts.Scorer,
		sticky:        opts.StickyLookup,
		fallback:      opts.ModelFallback,
		recentSuccess: opts.RecentSuccess,
		stop:          make(chan struct{}),
	}
	c.bitmap = newBitmapTable(opts.Capacity)
	c.stats = newStatsTable(opts.Capacity)
	// Full 位图联动：任一资源达限置位、回到限内清除。
	c.res = newResourceTable(opts.Capacity, c.bitmap.SetFull, c.bitmap.ClearFull)
	c.reg = newRegistry(opts.Capacity, c.res.Held)
	c.reg.onEvict = c.onRegistryEvict

	if opts.StartTickers {
		go c.runResetTicker()
		go c.runReconcileTicker()
	}
	return c
}

// onRegistryEvict 注册表 LRU 回收时清理全部派生槽位并通知监听者。
// 只操作位图/槽位（无 registry 回调），不会重入 registry 锁。
func (c *Cache) onRegistryEvict(ref NodeRef, id int32) {
	c.bitmap.ClearAvail(id)
	c.bitmap.ClearProbe(id)
	c.bitmap.ClearFull(id)
	c.stats.reset(id)
	c.res.reset(id)
	if c.opts.Invalidation != nil {
		c.opts.Invalidation.OnInvalidate(ref, id, InvalidationEvicted)
	}
}

// Register 注册节点，返回 dense id（容量满时回收 LRU 未活跃 id）。
func (c *Cache) Register(ref NodeRef) (int32, error) {
	id, _, err := c.reg.Register(ref)
	return id, err
}

// RegisterModel 注册/查询标准模型 dense id（NodeUseRecord.ModelID 用）。
func (c *Cache) RegisterModel(model string) int32 { return c.reg.ModelID(model) }

// NodeID 查询节点 dense id（未注册返回 0）。
func (c *Cache) NodeID(ref NodeRef) int32 { return c.reg.Get(ref) }

// NodeRef 查询 dense id 对应的 NodeRef。
func (c *Cache) NodeRef(id int32) (NodeRef, bool) { return c.reg.RefOf(id) }

// Invalidate 置脏（喂入路径②：NodeMirror pub/sub 失效广播接入点）：
// 清 Avail/Probe、State=unknown（在下一次喂入/对账前不可路由），并回调
// InvalidationListener 供集成者重拉权威值。
func (c *Cache) Invalidate(ref NodeRef) {
	id := c.reg.Get(ref)
	if id == 0 {
		return
	}
	c.bitmap.ClearAvail(id)
	c.bitmap.ClearProbe(id)
	c.stats.setState(id, StateUnknown)
	if c.opts.Invalidation != nil {
		c.opts.Invalidation.OnInvalidate(ref, id, InvalidationPubSub)
	}
}

// Selector 返回 R10.2 选择闭包。
func (c *Cache) Selector() NodeSelector { return c.Select }

// Updater 返回 R10.5 更新闭包（效应链尾部 / 探测结果挂接点）。
func (c *Cache) Updater() UpdaterFunc { return c.Update }

// TryAcquire 资源仪表组：无锁 CAS 占用（并发/FP slot/RPM）。
// 达限返回 false；成功后联动 Full 位图。
func (c *Cache) TryAcquire(nodeID int32, kind ResourceKind) bool {
	return c.res.TryAcquire(nodeID, kind, c.clock())
}

// Release 资源仪表组：释放占用（RPM 分钟窗消耗制，无释放）。
func (c *Cache) Release(nodeID int32, kind ResourceKind) { c.res.Release(nodeID, kind) }

// SetLimits 注入上限镜像（源 = credentials 表与 fpslot 配额）。
func (c *Cache) SetLimits(nodeID int32, conc, fp, rpm uint16) {
	c.res.SetLimits(nodeID, conc, fp, rpm)
}

// ResourceSlot 读取节点资源仪表快照（诊断/测试）。
func (c *Cache) ResourceSlot(nodeID int32) NodeResourceSlot { return c.res.load(nodeID) }

// StatsSlot 读取节点统计槽快照（诊断/测试）。
func (c *Cache) StatsSlot(nodeID int32) NodeStatsSlot { return c.stats.get(nodeID) }

// BitmapSnapshot 位图三平面快照（诊断/测试）。
func (c *Cache) BitmapSnapshot() NodeStateBitmap { return c.bitmap.Snapshot() }

// ResetWindow 手动窗口重置（ticker 之外的测试/运维入口）：
// RPM 分钟窗翻转 + 1h 统计窗翻转，各返回重置槽位数。
func (c *Cache) ResetWindow(now time.Time) (rpmReset, statsReset int) {
	rpmReset = c.res.resetMinute(now)
	statsReset = c.stats.sweep1h(now)
	return
}

func (c *Cache) clock() time.Time { return c.opts.Clock() }

// Close 停止内置 ticker（幂等）。
func (c *Cache) Close() {
	c.stopOnce.Do(func() { close(c.stop) })
}

func (c *Cache) runResetTicker() {
	if c.opts.ResetPeriod <= 0 {
		return
	}
	t := time.NewTicker(c.opts.ResetPeriod)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.ResetWindow(c.clock())
		}
	}
}

func (c *Cache) runReconcileTicker() {
	if c.opts.ReconcilePeriod <= 0 || c.opts.Authority == nil {
		return
	}
	t := time.NewTicker(c.opts.ReconcilePeriod)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.reconcileTick(context.Background())
		}
	}
}
