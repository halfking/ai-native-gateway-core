package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// cacheEntry 缓存条目，包含节点列表和过期时间（阶段 2 优化：TTL 机制）。
// covered 表示 nodes 是该订阅在 store.ListNodes 下的完整快照：只有 covered
// 条目可用于节点选择；未来的部分写入路径必须写 covered=false，选择路径会
// 因此回源 store，而不是拿到不完整的候选集。
type cacheEntry struct {
	nodes     []*Node
	expiresAt time.Time
	covered   bool
}

// activeSelection 记录 Manager 当前对外暴露的"已被选中的节点"，供 swap loop
// 主动探测并按策略切换。subscriptionID=nil 表示全局选择。
type activeSelection struct {
	subscriptionID   *int
	nodeID           int
	lastProbeAt      time.Time
	consecutiveFails int
}

// Manager 代理管理器
type Manager struct {
	store   Store
	parser  Parser
	checker HealthChecker
	// loadBalancer 在已排序的可用候选节点之间执行可配置的选择策略。
	loadBalancer *LoadBalancer
	// selectionMu serializes load-balancer state changes and selections.
	selectionMu sync.Mutex
	// transportFactory 按订阅缓存可复用的 http.Transport（Stage 2：避免每次
	// SelectBestNode/探活都新建 Transport 导致连接泄漏）。
	transportFactory *TransportFactory
	// metrics 代理子系统 Prometheus 指标。
	metrics *Metrics

	// 内存缓存：subscription_id -> cacheEntry（（阶段 2 优化：从 []*Node 改为带 TTL 的 cacheEntry）
	nodesCache sync.Map
	// cacheLocks protects copy-on-write updates for one subscription.
	cacheLocks sync.Map // subscription_id -> *sync.RWMutex
	// cacheRefreshes suppresses duplicate asynchronous refreshes for expired entries.
	cacheRefreshes sync.Map // subscription_id -> struct{}
	// nextHealthChecks is process-local scheduling state. It is intentionally not
	// persisted: after restart, nodes are safely eligible for a fresh probe.
	nextHealthChecks sync.Map // node_id -> time.Time
	// subscriptionBans 缓存订阅级禁用地区，避免每次 selectNode 都查 DB。
	subscriptionBans sync.Map // subscription_id -> []string
	// subscriptionPriorities 缓存订阅优先级（R46 F7：此前 Priority 入库+API
	// 可见但选路从不消费，是死配置）。语义沿用网关 manual_priority 惯例：
	// 数值越小优先级越高；0 = 未设置，与健康节点同桶不特殊对待。
	subscriptionPriorities sync.Map // subscription_id -> int
	// nodeProbes 把同一节点 ID 的探活串行化（HealthCheckNode / swapProbeOne /
	// healthCheckAllNodesInner 的 per-node 分支），防止并发的 checker.Check + write-back
	// 对同一个 *Node 对象产生竞态。值是 *sync.Mutex，按需 lazy 创建。
	nodeProbes sync.Map // node_id -> *sync.Mutex

	// 配置
	autoRefreshInterval   time.Duration
	healthCheckInterval   time.Duration
	unknownDomainStrategy string        // direct/proxy/probe
	cacheTTL              time.Duration // 缓存 TTL，默认 5 分钟
	autoDisableThreshold  int
	autoDisableEnabled    bool
	autoRecoverEnabled    bool

	// 当前生效的全局选择策略（持久化到 proxy_selection_policy 表）。
	selectionPolicy SelectionPolicy

	// activeSelection 跟踪每个 subscription 的当前选中节点，供 swapLoop 主动探测。
	activeMu  sync.Mutex
	active    map[string]*activeSelection // key = subscriptionKey(subscriptionID)
	activeSeq uint64                      // for stable IDs in metrics

	// 审计修复 (2026-08-29)：问题 7 - 增加 context 用于优雅停止后台 goroutine。
	ctx         context.Context
	cancel      context.CancelFunc
	stopCh      chan struct{}
	wg          sync.WaitGroup
	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
}

// NewManager 创建代理管理器
func NewManager(store Store, parser Parser, checker HealthChecker) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	metrics := NewMetrics(nil)
	factory := NewTransportFactory(nil)
	factory.metrics = metrics

	return &Manager{
		store:                 store,
		parser:                parser,
		checker:               checker,
		loadBalancer:          NewLoadBalancer(StrategyBestOnly),
		transportFactory:      factory,
		metrics:               metrics,
		selectionPolicy:       DefaultSelectionPolicy(),
		active:                make(map[string]*activeSelection),
		autoRefreshInterval:   time.Hour,
		healthCheckInterval:   5 * time.Minute,
		unknownDomainStrategy: "direct",
		cacheTTL:              5 * time.Minute,
		autoDisableThreshold:  3,
		autoDisableEnabled:    true,
		autoRecoverEnabled:    true,
		ctx:                   ctx,
		cancel:                cancel,
		stopCh:                make(chan struct{}),
	}
}

// Start 启动定时任务。多次调用只会启动一组后台任务。
func (m *Manager) Start() {
	m.lifecycleMu.Lock()
	if m.started || m.stopped {
		m.lifecycleMu.Unlock()
		return
	}
	m.started = true
	// Keep lifecycleMu while adding workers so Stop cannot call Wait concurrently
	// with WaitGroup.Add. The initial DB load is registered here too so Stop's
	// wg.Wait blocks until any in-flight loadAllNodesIntoCache returns (audit
	// #10 P2).
	m.wg.Add(4)
	go m.initialCacheLoad()
	go m.refreshLoop()
	go m.healthCheckLoop()
	go m.swapLoop()
	m.lifecycleMu.Unlock()
}

// initialCacheLoad runs the startup DB load off the lifecycleMu critical section
// so Stop() does not block on a slow ListNodes call.
func (m *Manager) initialCacheLoad() {
	defer m.wg.Done()
	if err := m.loadAllNodesIntoCache(); err != nil {
		slog.Error("proxy: failed to load nodes into cache", "error", err)
	}
}

// Stop 停止定时任务，并关闭可关闭的检查器及 Transport 工厂。
// 多次调用安全，不会重复关闭 channel 或资源。
func (m *Manager) Stop() {
	m.lifecycleMu.Lock()
	if m.stopped {
		m.lifecycleMu.Unlock()
		return
	}
	m.stopped = true
	m.cancel()
	close(m.stopCh)
	m.lifecycleMu.Unlock()

	m.wg.Wait()
	if closer, ok := m.checker.(interface{ Close() }); ok && closer != nil {
		closer.Close()
	}
	if m.transportFactory != nil {
		m.transportFactory.CloseIdleConnections()
	}
}

// SetLoadBalanceStrategy configures the strategy used by SelectNodeWithStrategy
// and SelectNodeWithLocation. SelectBestNode always remains best-only.
func (m *Manager) SetLoadBalanceStrategy(strategy LoadBalanceStrategy) {
	m.selectionMu.Lock()
	defer m.selectionMu.Unlock()
	affinity := AffinityAny
	if m.loadBalancer != nil {
		affinity = m.loadBalancer.locationAffinity
	}
	m.loadBalancer = NewLoadBalancer(strategy)
	m.loadBalancer.SetLocationAffinity(affinity)
}

// SetLocationAffinity configures location affinity for SelectNodeWithLocation.
func (m *Manager) SetLocationAffinity(policy LocationAffinityPolicy) {
	m.selectionMu.Lock()
	defer m.selectionMu.Unlock()
	if m.loadBalancer == nil {
		m.loadBalancer = NewLoadBalancer(StrategyBestOnly)
	}
	m.loadBalancer.SetLocationAffinity(policy)
}

// SetAutoDisablePolicy configures failure handling. The threshold always affects
// selection; the enablement flags are retained for health-check policy decisions.
func (m *Manager) SetAutoDisablePolicy(maxFailures int, autoDisable, autoRecover bool) {
	if maxFailures <= 0 {
		maxFailures = 3
	}
	m.selectionMu.Lock()
	m.autoDisableThreshold = maxFailures
	m.autoDisableEnabled = autoDisable
	m.autoRecoverEnabled = autoRecover
	m.selectionMu.Unlock()
}

// SetSelectionPolicy 替换当前生效的策略并下发到 LoadBalancer。
// 调用方负责自行持久化到 proxy_selection_policy 表（admin handler 写入后调用）。
func (m *Manager) SetSelectionPolicy(p SelectionPolicy) {
	m.selectionMu.Lock()
	defer m.selectionMu.Unlock()
	m.selectionPolicy = p
	m.autoDisableThreshold = p.AutoDisableThreshold
	m.autoDisableEnabled = p.AutoDisableEnabled
	m.autoRecoverEnabled = p.AutoRecoverEnabled
	if m.loadBalancer == nil {
		m.loadBalancer = NewLoadBalancer(p.LoadBalanceStrategy)
	} else {
		m.loadBalancer.SetStrategy(p.LoadBalanceStrategy)
	}
	m.loadBalancer.SetLocationAffinity(p.LocationAffinity)
}

// LoadSelectionPolicy 从 Store 读取全局策略并下发到 LoadBalancer。
// 用于网关启动后从数据库恢复上次保存的策略。
func (m *Manager) LoadSelectionPolicy(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	policy, err := m.store.LoadSelectionPolicy(ctx)
	if err != nil {
		// 非 PgStore 实现可以选择返回 ErrUnsupported；启动时退回到默认策略，
		// 不应阻塞 Manager 的健康检查 / 探活后台任务。
		if errors.Is(err, ErrUnsupported) {
			return nil
		}
		return err
	}
	m.SetSelectionPolicy(policy)
	return nil
}

// GetSelectionPolicy 返回当前生效的策略副本。
func (m *Manager) GetSelectionPolicy() SelectionPolicy {
	m.selectionMu.Lock()
	defer m.selectionMu.Unlock()
	return m.selectionPolicy
}

// SelectionState 汇总当前 Manager 选择/切流的可观测状态，给 admin UI 用。
type SelectionState struct {
	CurrentNodeID    int       `json:"current_node_id"`
	CurrentSubID     *int      `json:"current_subscription_id,omitempty"`
	LastProbeAt      time.Time `json:"last_probe_at"`
	LastProbeOK      bool      `json:"last_probe_ok"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	ProbeIntervalMs  int       `json:"probe_interval_ms"`
}

// CurrentSelection 返回当前被选中的节点状态（活跃选择中最近一次）。
func (m *Manager) CurrentSelection() *SelectionState {
	m.activeMu.Lock()
	defer m.activeMu.Unlock()
	var best *SelectionState
	for _, sel := range m.active {
		if sel == nil {
			continue
		}
		state := &SelectionState{
			CurrentNodeID:    sel.nodeID,
			LastProbeAt:      sel.lastProbeAt,
			ConsecutiveFails: sel.consecutiveFails,
		}
		if sel.subscriptionID != nil {
			id := *sel.subscriptionID
			state.CurrentSubID = &id
		}
		if best == nil || state.LastProbeAt.After(best.LastProbeAt) {
			best = state
		}
	}
	m.selectionMu.Lock()
	if best != nil {
		best.ProbeIntervalMs = m.selectionPolicy.SwapCheckIntervalMs
	}
	m.selectionMu.Unlock()
	return best
}

// SelectBestNode selects the best available node and intentionally ignores the
// configured load-balancing strategy for backward compatibility.
func (m *Manager) SelectBestNode(ctx context.Context, subscriptionID *int) (*Node, error) {
	return m.selectNode(ctx, subscriptionID, "", "", true)
}

// SelectNodeWithStrategy selects an available node using the configured strategy.
func (m *Manager) SelectNodeWithStrategy(ctx context.Context, subscriptionID *int, requestKey string) (*Node, error) {
	return m.selectNode(ctx, subscriptionID, requestKey, "", false)
}

// SelectNodeWithLocation selects an available node using the configured strategy
// and configured location-affinity policy.
func (m *Manager) SelectNodeWithLocation(ctx context.Context, subscriptionID *int, requestKey, preferredLocation string) (*Node, error) {
	return m.selectNode(ctx, subscriptionID, requestKey, preferredLocation, false)
}

func (m *Manager) selectNode(ctx context.Context, subscriptionID *int, requestKey, preferredLocation string, bestOnly bool) (*Node, error) {
	return m.selectNodeExcluding(ctx, subscriptionID, requestKey, preferredLocation, bestOnly, 0)
}

// selectNodeExcluding selects a node while omitting excludeNodeID when non-zero.
// It is used by ForceSwap so a threshold-triggered swap cannot reselect the
// failing active node.
func (m *Manager) selectNodeExcluding(ctx context.Context, subscriptionID *int, requestKey, preferredLocation string, bestOnly bool, excludeNodeID int) (_ *Node, err error) {
	startedAt := time.Now()
	result := "store_error"
	defer func() {
		if m.metrics != nil {
			m.metrics.ObserveNodeSelection(result, time.Since(startedAt).Seconds())
		}
	}()

	var candidates []*Node
	var cachePresent, cacheCovered bool
	if subscriptionID != nil {
		candidates, _, cachePresent, cacheCovered = m.getNodesFromCacheWithTTL(*subscriptionID, time.Now())
	} else {
		candidates, cachePresent = m.getAllActiveCachedNodes(time.Now())
	}
	// Coverage contract: a targeted selection may only trust a present cache
	// snapshot when its entry is a complete store.ListNodes result (covered).
	// Global selection always queries the store: per-entry coverage cannot prove
	// the aggregate spans every subscription (empty subscriptions may have no
	// entry, and the subscription set itself is not cached).
	if subscriptionID == nil || !cachePresent || !cacheCovered {
		candidates, err = m.store.ListNodes(ctx, subscriptionID)
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}
		if subscriptionID != nil {
			m.setCacheWithTTL(*subscriptionID, candidates, time.Now(), true)
		}
		// Store implementations may reuse mutable objects; isolate the selection
		// path from later health-check writes.
		candidates = cloneNodes(candidates)
	}

	m.selectionMu.Lock()
	threshold := m.autoDisableThreshold
	if threshold <= 0 {
		threshold = 3
	}
	// 订阅层禁用地区：每个候选节点按其所在订阅取出订阅 banned，做并集过滤。
	subscriptionBans := m.subscriptionBansSnapshot(subscriptionID)
	subPriorities := m.subscriptionPrioritiesSnapshot()
	m.selectionMu.Unlock()

	activeNodes := make([]*Node, 0, len(candidates))
	undialable := 0
	regionBanned := 0
	for _, node := range candidates {
		if node == nil || node.Status != "active" || node.PasswordDecryptFailed || node.ConsecutiveFailures >= threshold {
			continue
		}
		if excludeNodeID != 0 && node.ID == excludeNodeID {
			continue
		}
		if !node.Dialable() {
			undialable++
			continue
		}
		if IsRegionBanned(node.Location, subscriptionBans[node.SubscriptionID], node.BannedRegions) {
			regionBanned++
			continue
		}
		activeNodes = append(activeNodes, node)
	}
	if len(activeNodes) == 0 {
		switch {
		case regionBanned > 0:
			result = "region_banned"
			if m.metrics != nil {
				m.metrics.IncEgressSelection("region_banned")
			}
			return nil, fmt.Errorf("all %d candidate(s) excluded by region ban (subscription banned regions: %v)", regionBanned, subscriptionBans)
		case undialable > 0:
			result = "no_dialable"
			if m.metrics != nil {
				m.metrics.IncEgressSelection("undialable")
			}
			return nil, fmt.Errorf("no dialable proxy node: %d node(s) use protocols Go cannot proxy directly (trojan/vless/vmess/ss); expose them via a local mihomo/xray http or socks5 bridge and register that endpoint instead", undialable)
		default:
			result = "no_available"
			if m.metrics != nil {
				m.metrics.IncEgressSelection("none")
			}
			return nil, fmt.Errorf("no available active nodes")
		}
	}

	// R46 F7: 排序键第 2 位按订阅优先级升序分桶（小值优先，沿用网关
	// manual_priority 惯例；缺省 0 与"未设置"同桶）。放在 ConsecutiveFailures
	// 之后：健康等级优先于运营偏好——高优先订阅的连败节点不得压过低优先
	// 订阅的健康节点。
	subPrioritiesSort := func(n *Node) int {
		if p, ok := subPriorities[n.SubscriptionID]; ok {
			return p
		}
		return 0
	}
	sort.Slice(activeNodes, func(i, j int) bool {
		if activeNodes[i].ConsecutiveFailures != activeNodes[j].ConsecutiveFailures {
			return activeNodes[i].ConsecutiveFailures < activeNodes[j].ConsecutiveFailures
		}
		if pi, pj := subPrioritiesSort(activeNodes[i]), subPrioritiesSort(activeNodes[j]); pi != pj {
			return pi < pj
		}
		if activeNodes[i].SuccessRate != activeNodes[j].SuccessRate {
			return activeNodes[i].SuccessRate > activeNodes[j].SuccessRate
		}
		return activeNodes[i].ResponseTimeMs < activeNodes[j].ResponseTimeMs
	})

	var selected *Node
	if bestOnly {
		selected = activeNodes[0]
	} else {
		m.selectionMu.Lock()
		if m.loadBalancer == nil {
			m.loadBalancer = NewLoadBalancer(StrategyBestOnly)
		}
		subID := 0
		if subscriptionID != nil {
			subID = *subscriptionID
		}
		if preferredLocation != "" {
			selected = m.loadBalancer.SelectNodeWithLocation(activeNodes, subID, requestKey, preferredLocation)
		} else {
			selected = m.loadBalancer.SelectNode(activeNodes, subID, requestKey)
		}
		m.selectionMu.Unlock()
	}
	if selected == nil {
		result = "no_available"
		if m.metrics != nil {
			m.metrics.IncEgressSelection("none")
		}
		return nil, fmt.Errorf("no available active nodes")
	}
	result = "success"
	if m.metrics != nil {
		m.metrics.IncEgressSelection("dialable")
	}
	m.recordActiveSelection(subscriptionID, selected)
	return selected, nil
}

// subscriptionBansSnapshot 返回 subscription_id → banned regions 的快照。
// 调用方需持有 m.selectionMu。
func (m *Manager) subscriptionBansSnapshot(filter *int) map[int][]string {
	out := make(map[int][]string)
	m.subscriptionBans.Range(func(key, value interface{}) bool {
		subID, ok := key.(int)
		if !ok {
			return true
		}
		if filter != nil && subID != *filter {
			// 当筛选特定订阅时，全局 cache 中的"其它订阅"也一起返回，便于
			// 全局 SelectBestNode 调用方；不会泄漏禁用到候选之外。
		}
		if bans, ok := value.([]string); ok {
			out[subID] = bans
		}
		return true
	})
	return out
}

// recordActiveSelection 记录一个被选中的节点，供 swapLoop 主动探测。
func (m *Manager) recordActiveSelection(subscriptionID *int, node *Node) {
	if node == nil {
		return
	}
	key := selectionKey(subscriptionID)
	m.activeMu.Lock()
	defer m.activeMu.Unlock()
	cur, ok := m.active[key]
	if !ok || cur.nodeID != node.ID {
		cur = &activeSelection{
			subscriptionID: subscriptionID,
			nodeID:         node.ID,
		}
		m.active[key] = cur
	}
	if cur.lastProbeAt.IsZero() {
		cur.lastProbeAt = time.Now()
	}
}

// selectionKey 把 subscription_id 序列化为 map key（nil → "global"）。
func selectionKey(subscriptionID *int) string {
	if subscriptionID == nil {
		return "global"
	}
	return fmt.Sprintf("sub:%d", *subscriptionID)
}

// forgetActiveSelection 在节点/订阅被删除时清理，避免 swapLoop 持续探测已删节点。
func (m *Manager) forgetActiveSelection(subscriptionID *int, nodeID int) {
	key := selectionKey(subscriptionID)
	m.activeMu.Lock()
	defer m.activeMu.Unlock()
	cur, ok := m.active[key]
	if !ok || cur.nodeID != nodeID {
		return
	}
	delete(m.active, key)
}

// RequiresProxy 判断域名是否需要代理
func (m *Manager) RequiresProxy(ctx context.Context, baseURL string) bool {
	domain := extractDomain(baseURL)
	if domain == "" {
		return false
	}

	// 查询域名配置
	domainRecord, err := m.store.GetDomain(ctx, domain)
	if err != nil || domainRecord == nil {
		// 未知域名，使用默认策略
		return m.unknownDomainStrategy == "proxy"
	}

	return domainRecord.RequiresProxy
}

// RefreshSubscription 刷新订阅
func (m *Manager) RefreshSubscription(ctx context.Context, subscriptionID int) error {
	startTime := time.Now()

	// 1. 查询订阅配置
	sub, err := m.store.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return fmt.Errorf("get subscription: %w", err)
	}

	if sub.Status != "active" {
		return fmt.Errorf("subscription is not active")
	}

	// 2. 解析订阅
	slog.Info("proxy: refreshing subscription", "id", subscriptionID, "name", sub.Name)
	nodes, err := m.parser.Parse(ctx, sub.SubscribeURL)
	if err != nil {
		// 更新订阅错误状态
		sub.LastFetchAt = time.Now()
		sub.LastFetchStatus = "failed"
		sub.LastError = SanitizeSubscriptionError(err.Error(), sub.SubscribeURL)
		_ = m.store.UpdateSubscription(ctx, sub)

		// 记录失败指标
		if m.metrics != nil {
			m.metrics.ObserveSubscriptionRefresh("failed", time.Since(startTime).Seconds())
		}
		return fmt.Errorf("parse subscription: %s", SanitizeSubscriptionError(err.Error(), sub.SubscribeURL))

	}

	// 3. 更新数据库（事务保护，审计修复 2026-08-29 问题 1）
	for _, node := range nodes {
		node.SubscriptionID = subscriptionID
	}

	// 使用事务方法原子性地删除旧节点并插入新节点，避免中断导致订阅变空。
	if pgStore, ok := m.store.(*PgStore); ok {
		// 审计 #9 P2：单事务同时提交节点集合 + 订阅 metadata，
		// 避免之前 RefreshSubscriptionNodes + UpdateSubscription 两步间
		// 出现 "nodes 已刷新但 metadata 仍是上一次状态" 的不一致窗口。
		if err := pgStore.RefreshSubscriptionAndMetadata(ctx, subscriptionID, nodes, sub); err != nil {
			sub.NodeCount = 0
			sub.LastFetchAt = time.Now()
			sub.LastFetchStatus = "failed"
			sub.LastError = SanitizeSubscriptionError(
				fmt.Sprintf("transaction failed: %v", err), sub.SubscribeURL)

			// 事务失败后尝试补救落库 metadata（best-effort；不再次尝试事务）
			_ = m.store.UpdateSubscription(ctx, sub)

			// 记录失败指标
			if m.metrics != nil {
				m.metrics.ObserveSubscriptionRefresh("failed", time.Since(startTime).Seconds())
			}
			return fmt.Errorf("refresh nodes and metadata in transaction: %w", err)
		}
	} else {
		// 回退到非事务方法（用于测试或非 PgStore 实现）
		if err := m.store.DeleteNodesBySubscription(ctx, subscriptionID); err != nil {
			return fmt.Errorf("delete old nodes: %w", err)
		}
		var createErrs []string
		for _, node := range nodes {
			if err := m.store.CreateNode(ctx, node); err != nil {
				createErrs = append(createErrs, fmt.Sprintf("%s: %v", node.Name, err))
				slog.Warn("proxy: failed to create node", "error", err, "name", node.Name)
			}
		}
		if len(createErrs) > 0 {
			sub.NodeCount = len(nodes) - len(createErrs)
			sub.LastFetchAt = time.Now()
			sub.LastFetchStatus = "failed"
			sub.LastError = SanitizeSubscriptionError(
				fmt.Sprintf("persisted %d/%d nodes; %d failed: %s",
					len(nodes)-len(createErrs), len(nodes), len(createErrs), strings.Join(createErrs, "; ")), sub.SubscribeURL)

			_ = m.store.UpdateSubscription(ctx, sub)

			// 记录失败指标
			if m.metrics != nil {
				m.metrics.ObserveSubscriptionRefresh("failed", time.Since(startTime).Seconds())
			}
			return fmt.Errorf("persist nodes: %d/%d failed: %s",
				len(createErrs), len(nodes), strings.Join(createErrs, "; "))
		}
	}

	// 4. 更新订阅状态（PgStore 路径已通过 RefreshSubscriptionAndMetadata 同步落库）
	sub.NodeCount = len(nodes)
	sub.LastFetchAt = time.Now()
	sub.LastFetchStatus = "success"
	sub.LastError = ""
	if _, isPg := m.store.(*PgStore); !isPg {
		if err := m.store.UpdateSubscription(ctx, sub); err != nil {
			return fmt.Errorf("update subscription: %w", err)
		}
	}

	// 5. 刷新内存缓存；仅在缓存成功更新后失效旧的订阅 Transport，
	// 避免后续请求继续复用已被订阅刷新替换的节点连接池。
	if m.loadNodesIntoCache(ctx, subscriptionID) {
		m.InvalidateTransport(subscriptionID)
	}

	// 6. 同步订阅级禁用地区到本地缓存，供 selectNode 直接使用。
	m.refreshSubscriptionBans(ctx, subscriptionID)

	// 记录成功指标
	if m.metrics != nil {
		m.metrics.ObserveSubscriptionRefresh("success", time.Since(startTime).Seconds())
	}

	slog.Info("proxy: subscription refreshed", "id", subscriptionID, "node_count", len(nodes))
	return nil
}

// refreshSubscriptionBans 把订阅级 banned_regions 同步到内存缓存。
// LoadCache / RefreshSubscription / Subscription PUT 后调用，保证 selectNode
// 看到最新的禁用集合，避免仍命中已被禁用的节点。
func (m *Manager) refreshSubscriptionBans(ctx context.Context, subscriptionID int) {
	sub, err := m.store.GetSubscription(ctx, subscriptionID)
	if err != nil {
		slog.Warn("proxy: refresh subscription bans: get failed", "id", subscriptionID, "error", err)
		return
	}
	if len(sub.BannedRegions) == 0 {
		m.subscriptionBans.Delete(subscriptionID)
		return
	}
	bans := make([]string, len(sub.BannedRegions))
	copy(bans, sub.BannedRegions)
	m.subscriptionBans.Store(subscriptionID, bans)
}

// refreshAllSubscriptionBans 启动时或 ReloadCache 时调用，把全部订阅的
// banned_regions 加载到内存。
func (m *Manager) refreshAllSubscriptionBans(ctx context.Context) {
	subs, err := m.store.ListSubscriptions(ctx)
	if err != nil {
		slog.Warn("proxy: refresh all subscription bans: list failed", "error", err)
		return
	}
	seen := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		seen[sub.ID] = struct{}{}
		if len(sub.BannedRegions) == 0 {
			m.subscriptionBans.Delete(sub.ID)
		} else {
			bans := make([]string, len(sub.BannedRegions))
			copy(bans, sub.BannedRegions)
			m.subscriptionBans.Store(sub.ID, bans)
		}
		// R46 F7: 优先级随同一轮订阅刷新维护；优先级变更走 UpdateSubscription
		// → ReloadCache → 本函数，覆盖全部生效路径。
		m.subscriptionPriorities.Store(sub.ID, sub.Priority)
	}
	// 清掉已删除订阅的残留。
	m.subscriptionBans.Range(func(key, _ interface{}) bool {
		id, ok := key.(int)
		if !ok {
			return true
		}
		if _, exists := seen[id]; !exists {
			m.subscriptionBans.Delete(id)
			m.subscriptionPriorities.Delete(id)
		}
		return true
	})
}

// subscriptionPrioritiesSnapshot 返回订阅优先级快照（selectNodeExcluding
// 排序键用）。
func (m *Manager) subscriptionPrioritiesSnapshot() map[int]int {
	out := make(map[int]int)
	m.subscriptionPriorities.Range(func(key, value interface{}) bool {
		if id, ok := key.(int); ok {
			if p, ok := value.(int); ok {
				out[id] = p
			}
		}
		return true
	})
	return out
}

// healthCheckPolicy returns a consistent snapshot while callers may update the
// policy at runtime through SetAutoDisablePolicy.
func (m *Manager) healthCheckPolicy() (threshold int, disable, recover bool) {
	m.selectionMu.Lock()
	defer m.selectionMu.Unlock()
	return m.autoDisableThreshold, m.autoDisableEnabled, m.autoRecoverEnabled
}

// skipReason 描述 health-check 过滤节点时跳过的原因。
type skipReason string

const (
	skipReasonStatus        skipReason = "status"
	skipReasonUndialable    skipReason = "undialable"
	skipReasonPwdDecrypt    skipReason = "password_decrypt_failed"
	skipReasonProxyURLEmpty skipReason = "proxy_url_empty"
)

// shouldSkipForHealthCheck 是所有 health-check 入口统一的节点过滤闸门。
//
// 审计修复 (2026-09-09 P2-#6)：PasswordDecryptFailed 检查此前只覆盖
// HealthCheckNode / HealthCheckSubscriptionNow 单点；swapProbeOne 与
// healthCheckAllNodesInner 会继续以密文密码探测——代理返回 407 的同时
// 静默消耗 1 次失败计数。统一到一处，四个入口共用同一语义。
func (m *Manager) shouldSkipForHealthCheck(node *Node) (skipReason, bool) {
	if node == nil {
		return "nil", true
	}
	if node.Status != "active" && node.Status != "unhealthy" {
		return skipReasonStatus, true
	}
	if !node.Dialable() {
		return skipReasonUndialable, true
	}
	if node.PasswordDecryptFailed {
		return skipReasonPwdDecrypt, true
	}
	return "", false
}

// applyHealthCheckResult applies the shared node state transition for a health check.
// Probe metrics and logging remain with the individual health-check paths.
func (m *Manager) applyHealthCheckResult(node *Node, ok bool, latency int, checkedAt time.Time) {
	threshold, autoDisable, autoRecover := m.healthCheckPolicy()
	node.LastHealthCheckAt = checkedAt
	if ok {
		node.LastHealthCheckStatus = "success"
		node.ResponseTimeMs = latency
		node.ConsecutiveFailures = 0
		node.SuccessRate = node.SuccessRate*0.9 + 0.1
		if node.Status == "active" || (node.Status == "unhealthy" && autoRecover) {
			node.Status = "active"
		}
		return
	}

	node.LastHealthCheckStatus = "failed"
	node.ConsecutiveFailures++
	if autoDisable && node.ConsecutiveFailures >= threshold {
		node.Status = "unhealthy"
	}
}

// HealthCheckNode 健康检查单个节点
//
// 审计修正 (2026-09-09 P0)：GetNode 必须在 nodeProbeLock 内执行。原实现
// 在锁外读快照（CF=0）→ 锁内 apply+落库（CF=1），两个并发各自读到 0、
// 各写 1，一次失败被静默吞掉。读-改-写全程持锁才原子。
func (m *Manager) HealthCheckNode(ctx context.Context, nodeID int) error {
	mu := m.nodeProbeLock(nodeID)
	mu.Lock()
	defer mu.Unlock()

	node, err := m.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node: %w", err)
	}
	if reason, skip := m.shouldSkipForHealthCheck(node); skip {
		// 审计修复 (2026-09-09 P2-#6)：统一闸门（原散落的 pwd-decrypt 单点检查）。
		if reason == skipReasonPwdDecrypt && m.metrics != nil {
			m.metrics.IncPasswordDecryptFailed()
		}
		return fmt.Errorf("health check node %d: %s", nodeID, reason)
	}

	startTime := time.Now()
	responseTimeMs, err := m.checker.Check(ctx, node)
	elapsed := time.Since(startTime)

	if err != nil {
		m.applyHealthCheckResult(node, false, responseTimeMs, time.Now())

		// 记录失败指标
		if m.metrics != nil {
			m.metrics.IncHealthCheck("failed")
			m.metrics.IncHealthFailure()
			m.metrics.ObserveHealthCheckDuration(elapsed.Seconds())
			m.metrics.ObserveNodeConsecutiveFailures(node.ConsecutiveFailures)
		}

		slog.Warn("proxy: health check failed",
			"node", node.Name,
			"error", err,
			"elapsed_ms", elapsed.Milliseconds())
	} else {
		m.applyHealthCheckResult(node, true, responseTimeMs, time.Now())

		// 记录成功指标
		if m.metrics != nil {
			m.metrics.IncHealthCheck("success")
			m.metrics.ObserveHealthCheckDuration(elapsed.Seconds())
			m.metrics.ObserveNodeResponseTime(float64(responseTimeMs))
			m.metrics.ObserveNodeConsecutiveFailures(0)
		}
	}

	if err := m.store.UpdateNode(ctx, node); err != nil {
		return fmt.Errorf("update node: %w", err)
	}

	// 更新缓存
	m.updateNodeInCache(node)

	// 把这次探活结果也回灌给 swap 状态。
	subID := node.SubscriptionID
	m.recordProbeResult(&subID, node.ID, err == nil)

	return nil
}

// recordProbeResult 由 HealthCheckNode / swapLoop 调用，更新当前 active 状态的
// 最近探测时间与连续失败计数。
func (m *Manager) recordProbeResult(subscriptionID *int, nodeID int, ok bool) {
	key := selectionKey(subscriptionID)
	m.activeMu.Lock()
	defer m.activeMu.Unlock()
	cur, ok2 := m.active[key]
	if !ok2 || cur.nodeID != nodeID {
		return
	}
	cur.lastProbeAt = time.Now()
	if ok {
		cur.consecutiveFails = 0
	} else {
		cur.consecutiveFails++
	}
}

// ReloadCache 重新从数据库加载节点缓存。
// 通过 API 增删节点后必须调用，否则 SelectBestNode 会命中过期缓存。
// 审计修复 (2026-08-29)：问题 5 - 原子替换缓存，避免清空和加载之间的空窗期。
func (m *Manager) ReloadCache() error {
	// 先加载新数据
	ctx := context.Background()
	nodes, err := m.store.ListNodes(ctx, nil)
	if err != nil {
		return err
	}

	// 按订阅 ID 分组
	nodesBySubscription := make(map[int][]*Node)
	for _, node := range nodes {
		nodesBySubscription[node.SubscriptionID] = append(
			nodesBySubscription[node.SubscriptionID], node)
	}

	// 原子替换：先删除不存在的订阅，再更新/新增
	// 审计修复 (2026-08-30)：同步清理 cacheLocks 中的孤儿锁，防止长期运行的内存泄漏
	// 审计修复 (2026-09-01 P2)：同步清理负载均衡器的 per-subscription 游标，
	// 否则 roundRobinIndex/weightedCurrent 只增不减，节点全下线的订阅永久泄漏条目。
	// R4 #13 negative-cache 契约：先为"store 中存在但无节点"的订阅安装
	// present-but-empty 条目；孤儿清扫对这些订阅（含空订阅）豁免，只清
	// store 里已不存在的订阅。
	knownSubIDs := m.installNegativeCacheEntries(ctx, nodesBySubscription)

	m.nodesCache.Range(func(key, _ interface{}) bool {
		subID := key.(int)
		if _, exists := nodesBySubscription[subID]; !exists {
			if _, known := knownSubIDs[subID]; known {
				return true
			}
			m.nodesCache.Delete(subID)
			m.cacheLocks.Delete(subID)
			m.forgetLoadBalancerState(subID)
			m.subscriptionBans.Delete(subID)
			// Orphan-sweep of m.active for this subscription runs in the block below.
		}
		return true
	})

	now := time.Now()
	for subID, nodes := range nodesBySubscription {
		m.setCacheWithTTL(subID, nodes, now, true)
	}

	// 重新加载订阅级禁用地区。
	m.refreshAllSubscriptionBans(ctx)
	// 清掉已不存在的节点的 active selection。
	m.activeMu.Lock()
	for key, sel := range m.active {
		if sel == nil {
			continue
		}
		if sel.subscriptionID != nil {
			if _, exists := nodesBySubscription[*sel.subscriptionID]; !exists {
				delete(m.active, key)
				continue
			}
		}
		found := false
		for _, n := range nodes {
			if n.ID == sel.nodeID {
				found = true
				break
			}
		}
		if !found {
			delete(m.active, key)
		}
	}
	m.activeMu.Unlock()

	return nil
}

// InvalidateTransport 使订阅的缓存 Transport 失效并关闭其空闲连接。
// 供节点/订阅删除后调用，避免持有指向已删节点的陈旧连接池。
func (m *Manager) InvalidateTransport(subscriptionID int) {
	if m.transportFactory != nil {
		m.transportFactory.Invalidate(subscriptionID)
	}
}

// GetProxyTransport 获取代理 Transport（按订阅缓存复用，避免每次新建导致连接泄漏）。
func (m *Manager) GetProxyTransport(ctx context.Context, subscriptionID *int) (*http.Transport, error) {
	node, err := m.SelectBestNode(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	return m.GetProxyTransportForNode(subscriptionID, node)
}

// GetProxyTransportForNode 为已选节点构造或取得订阅缓存的 Transport。
// 调用方先完成节点选择时应使用此方法，确保 Transport 与节点标签指向同一出口。
func (m *Manager) GetProxyTransportForNode(subscriptionID *int, node *Node) (*http.Transport, error) {
	if node == nil {
		return nil, fmt.Errorf("proxy node is required")
	}
	proxyURLStr := node.ProxyURL()
	if proxyURLStr == "" {
		return nil, fmt.Errorf("unsupported proxy protocol: %s", node.Protocol)
	}

	subID := 0
	if subscriptionID != nil {
		subID = *subscriptionID
	}
	// 工厂按订阅缓存 Transport，仅当已选节点的代理 URL 变化时才重建；
	// 命中同一订阅的多次请求共享连接池。
	return m.transportFactory.Get(subID, proxyURLStr)
}

// ForceSwap 主动重选一个不同于当前 active 的最优节点（不依赖外部触发）。
// 成功切换时使对应订阅 Transport 失效；没有备用节点时保留当前 active 状态，
// 让连续失败计数继续反映真实故障，而不会因重新选择同一节点而被重置。
func (m *Manager) ForceSwap(ctx context.Context, subscriptionID *int) (*Node, error) {
	subID := 0
	if subscriptionID != nil {
		subID = *subscriptionID
	}
	key := selectionKey(subscriptionID)
	m.activeMu.Lock()
	current := m.active[key]
	excludeNodeID := 0
	if current != nil {
		excludeNodeID = current.nodeID
	}
	m.activeMu.Unlock()

	candidate, err := m.selectNodeExcluding(ctx, subscriptionID, "", "", true, excludeNodeID)
	if err != nil {
		if excludeNodeID != 0 {
			return nil, fmt.Errorf("no alternative proxy node for swap from node %d: %w", excludeNodeID, err)
		}
		return nil, err
	}

	m.activeMu.Lock()
	cur, existed := m.active[key]
	m.active[key] = &activeSelection{
		subscriptionID: subscriptionID,
		nodeID:         candidate.ID,
		lastProbeAt:    time.Now(),
	}
	m.activeMu.Unlock()
	if existed && cur != nil && cur.nodeID != candidate.ID {
		m.InvalidateTransport(subID)
		if m.metrics != nil {
			m.metrics.IncAutoSwap("forced")
		}
		slog.Info("proxy: forced swap", "from", cur.nodeID, "to", candidate.ID)
	} else if m.metrics != nil {
		m.metrics.IncAutoSwap("forced")
	}
	return candidate, nil
}

// HealthCheckAllNodesNow 在 HTTP 路径上供 admin "批量探活" 按钮使用；与
// healthCheckAllNodes 逻辑一致，但只跑一次、不修改 nextHealthChecks 节流表。
func (m *Manager) HealthCheckAllNodesNow(ctx context.Context) HealthCheckSummary {
	return m.healthCheckAllNodesInner(ctx, true)
}

// HealthCheckSubscriptionNow 对单个订阅下全部可拨号节点做并发探活并写回。
// 用于 admin UI 上的"对单个订阅批量探活"按钮。
func (m *Manager) HealthCheckSubscriptionNow(ctx context.Context, subscriptionID int) (HealthCheckSummary, error) {
	subID := subscriptionID
	nodes, err := m.store.ListNodes(ctx, &subID)
	if err != nil {
		return HealthCheckSummary{}, fmt.Errorf("list nodes: %w", err)
	}
	var toCheck []*Node
	summary := HealthCheckSummary{Total: len(nodes)}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		// 审计修复 (2026-09-09 P2-#6)：统一闸门；Skipped 计数保持原语义
		//（undialable 与 pwd-decrypt 均计入）。
		if reason, skip := m.shouldSkipForHealthCheck(n); skip {
			if reason == skipReasonUndialable || reason == skipReasonPwdDecrypt {
				summary.Skipped++
			}
			if reason == skipReasonPwdDecrypt && m.metrics != nil {
				m.metrics.IncPasswordDecryptFailed()
			}
			continue
		}
		toCheck = append(toCheck, n)
	}
	for res := range m.checker.CheckConcurrent(ctx, toCheck, 16) {
		// 审计修正 (2026-09-09 P0)：GetNode 在锁内，保证读-改-写原子。
		mu := m.nodeProbeLock(res.NodeID)
		mu.Lock()
		node, gerr := m.store.GetNode(ctx, res.NodeID)
		if gerr != nil {
			mu.Unlock()
			slog.Warn("proxy: health check subscription: get node failed", "node_id", res.NodeID, "error", gerr)
			continue
		}
		m.applyHealthCheckResult(node, res.OK, res.Latency, res.CheckedAt)
		if res.OK {
			summary.OK++
			summary.AvgMs += res.Latency
			if res.Latency > summary.MaxMs {
				summary.MaxMs = res.Latency
			}
		} else {
			summary.Failed++
		}
		if uerr := m.store.UpdateNode(ctx, node); uerr != nil {
			mu.Unlock()
			slog.Warn("proxy: health check subscription: update node failed", "node_id", node.ID, "error", uerr)
			continue
		}
		m.updateNodeInCache(node)
		m.recordProbeResult(&subID, node.ID, res.OK)
		mu.Unlock()
	}
	if summary.OK > 0 {
		summary.AvgMs /= summary.OK
	}
	if m.metrics != nil {
		for i := 0; i < summary.Failed; i++ {
			m.metrics.IncHealthFailure()
		}
		m.metrics.SetNodeCounts(summary.Total, summary.Total-summary.Skipped, summary.Total-summary.OK-summary.Skipped)
	}
	return summary, nil
}

// 内部方法

func (m *Manager) refreshLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.autoRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 使用带超时的 context，避免阻塞 stopCh
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
			m.refreshAllSubscriptions(ctx)
			cancel()
		case <-m.stopCh:
			return
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *Manager) healthCheckLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 使用带超时的 context，避免阻塞 stopCh
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
			m.healthCheckAllNodesInner(ctx, false)
			cancel()
		case <-m.stopCh:
			return
		case <-m.ctx.Done():
			return
		}
	}
}

// swapLoop 周期性探测当前 active 节点；连续失败达到 SwapFailureThreshold 则
// 触发 ForceSwap 让下个请求走新节点 + 失效订阅 Transport。
//
// 审计修复 (2026-09-09 P2-#11)：单次 swap tick 使用独立 30s timeout context
// （与 refreshLoop / healthCheckLoop 对齐）——Stop 触发 cancel() 后 in-flight
// 探测经 swapProbeOne 的 nodeProbeLock 内 checker.Check(ctx,…) 立即收到
// ctx.Done() 提前退出，不再被 15s probe 超时逐节点串行拖住 Stop。
func (m *Manager) swapLoop() {
	defer m.wg.Done()
	for {
		m.selectionMu.Lock()
		intervalMs := m.selectionPolicy.SwapCheckIntervalMs
		threshold := m.selectionPolicy.SwapFailureThreshold
		m.selectionMu.Unlock()
		if intervalMs <= 0 {
			intervalMs = 30000
		}
		if threshold <= 0 {
			threshold = 2
		}
		select {
		case <-time.After(time.Duration(intervalMs) * time.Millisecond):
		case <-m.stopCh:
			return
		case <-m.ctx.Done():
			return
		}
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		m.runSwapTick(ctx, threshold)
		cancel()
	}
}

// runSwapTick 执行一次 swap tick：对每个 active selection 做主动探测并按策略切流。
func (m *Manager) runSwapTick(ctx context.Context, threshold int) {
	m.activeMu.Lock()
	if len(m.active) == 0 {
		m.activeMu.Unlock()
		return
	}
	pending := make([]*activeSelection, 0, len(m.active))
	for _, sel := range m.active {
		if sel != nil {
			pending = append(pending, sel)
		}
	}
	m.activeMu.Unlock()
	for _, sel := range pending {
		m.swapProbeOne(ctx, sel, threshold)
	}
}

// swapProbeOne 对单个 active selection 做一次主动探测。
//
// 审计修正 (2026-09-09 P0)：GetNode 在 nodeProbeLock 内执行，保证
// 读-改-写（读快照 → apply → 落库 → 写缓存）对同一 node_id 原子。
func (m *Manager) swapProbeOne(ctx context.Context, sel *activeSelection, threshold int) {
	if sel == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// 串行化与 HealthCheckNode / healthCheckAllNodesInner 的并发探活，
	// 防止 checker.Check 与 update-back 在同一 *Node 上重叠写。
	mu := m.nodeProbeLock(sel.nodeID)
	mu.Lock()
	defer mu.Unlock()
	node, err := m.store.GetNode(ctx, sel.nodeID)
	if err != nil {
		// 节点已删，清理。
		m.forgetActiveSelection(sel.subscriptionID, sel.nodeID)
		return
	}
	if reason, skip := m.shouldSkipForHealthCheck(node); skip {
		// 审计修复 (2026-09-09 P2-#6)：PasswordDecryptFailed 节点不再以密文
		// 密码探测（代理 407 + 静默 +1 失败计数）；status/undialable 同样跳过。
		if reason == skipReasonPwdDecrypt && m.metrics != nil {
			m.metrics.IncPasswordDecryptFailed()
		}
		return
	}
	if node.Status == "unhealthy" {
		// 已被全局探活标记为 unhealthy：视为失败 1 次计入，连续达阈值即切流。
		// 同步写回内存状态与持久层，避免 selectNode 的 threshold 过滤看不到进展。
		m.applyHealthCheckResult(node, false, 0, time.Now())
		if uerr := m.store.UpdateNode(ctx, node); uerr == nil {
			m.updateNodeInCache(node)
		} else {
			slog.Warn("proxy: swap probe: update node failed", "node_id", node.ID, "error", uerr)
		}
		m.recordProbeResult(sel.subscriptionID, sel.nodeID, false)
	} else {
		latency, err := m.checker.Check(ctx, node)
		m.applyHealthCheckResult(node, err == nil, latency, time.Now())
		if uerr := m.store.UpdateNode(ctx, node); uerr == nil {
			m.updateNodeInCache(node)
		} else {
			slog.Warn("proxy: swap probe: update node failed", "node_id", node.ID, "error", uerr)
		}
		m.recordProbeResult(sel.subscriptionID, sel.nodeID, err == nil)
		if err != nil {
			slog.Warn("proxy: swap probe failed",
				"node", node.Name, "error", redactErr(err), "latency_ms", latency)
		}
	}

	// 看看是否需要切流。
	m.activeMu.Lock()
	cur, ok := m.active[selectionKey(sel.subscriptionID)]
	m.activeMu.Unlock()
	if !ok || cur == nil || cur.nodeID != sel.nodeID {
		return
	}
	if cur.consecutiveFails >= threshold {
		if _, err := m.ForceSwap(ctx, sel.subscriptionID); err != nil {
			slog.Warn("proxy: auto swap failed", "subscription_id", sel.subscriptionID, "error", err)
		} else if m.metrics != nil {
			m.metrics.IncAutoSwap("auto")
		}
	}
}

func (m *Manager) refreshAllSubscriptions(ctx context.Context) {
	subs, err := m.store.ListSubscriptions(ctx)
	if err != nil {
		slog.Error("proxy: failed to list subscriptions", "error", err)
		return
	}

	active, inactive := 0, 0
	for _, sub := range subs {
		if sub.Status == "active" {
			active++
			if err := m.RefreshSubscription(ctx, sub.ID); err != nil {
				slog.Error("proxy: failed to refresh subscription",
					"id", sub.ID,
					"name", sub.Name,
					"error", err)
			}
		} else {
			inactive++
		}
	}
	if m.metrics != nil {
		m.metrics.SetSubscriptions(active, inactive)
	}

}

func (m *Manager) healthCheckAllNodes(ctx context.Context) {
	m.healthCheckAllNodesInner(ctx, false)
}

// healthCheckAllNodesInner 是健康检查全节点的核心实现；forcible=true 时跳过
// nextHealthChecks 节流，供 admin "批量探活"按钮使用。
func (m *Manager) healthCheckAllNodesInner(ctx context.Context, forcible bool) HealthCheckSummary {
	startTime := time.Now()
	now := time.Now()

	// 阶段 2 优化：全局批量探活，按 URL 去重 + 智能探活间隔
	// 1. 收集所有活跃订阅的节点
	allNodes, err := m.store.ListNodes(ctx, nil)
	if err != nil {
		slog.Error("proxy: failed to list all nodes for health check", "error", err)
		return HealthCheckSummary{}
	}

	// 2. 过滤需要探活的节点（基于智能间隔），并按代理+探活目标分组去重。
	// 同一代理入口请求不同 HealthCheckURL 时不能共享探测结果。
	type nodeGroup struct {
		probeKey string
		nodes    []*Node // 相同代理入口和探活目标的节点
	}
	urlToGroup := make(map[string]*nodeGroup)
	totalNodes := 0
	skippedUndialable := 0
	skippedByInterval := 0 // 由于智能间隔跳过的节点
	healthyNodes := 0
	unhealthyNodes := 0

	for _, node := range allNodes {
		// 只检查 active 和 unhealthy 状态的节点
		if node == nil || (node.Status != "active" && node.Status != "unhealthy") {
			continue
		}

		totalNodes++

		// 审计修复 (2026-09-09 P2-#6)：过滤走统一闸门，包含
		// Dialable/PasswordDecryptFailed 层；pwd-decrypt 节点记录指标并
		// 计入 skipped，不再以密文密码探测。
		if reason, skip := m.shouldSkipForHealthCheck(node); skip {
			switch reason {
			case skipReasonUndialable:
				skippedUndialable++
			case skipReasonPwdDecrypt:
				if m.metrics != nil {
					m.metrics.IncPasswordDecryptFailed()
				}
				skippedUndialable++
			}
			continue
		}

		if !forcible {
			if next, ok := m.nextHealthCheckAt(node.ID); ok && now.Before(next) {
				skippedByInterval++
				continue
			}
		}

		proxyURL := node.ProxyURL()
		if proxyURL == "" {
			continue
		}

		// 分组键与探测目标同源（healthTargetURL）：同代理 + 同探活目标
		// 才共享探测结果，空白/默认值差异不再拆组或误并组。
		probeKey := proxyURL + "\x00" + healthTargetURL(node)
		if _, exists := urlToGroup[probeKey]; !exists {
			urlToGroup[probeKey] = &nodeGroup{
				probeKey: probeKey,
				nodes:    []*Node{node},
			}
		} else {
			urlToGroup[probeKey].nodes = append(urlToGroup[probeKey].nodes, node)
		}

		// 统计健康/不健康节点数
		if node.ConsecutiveFailures == 0 {
			healthyNodes++
		} else {
			unhealthyNodes++
		}
	}

	// 3. 构建去重后的节点列表（每个代理+探活目标只取一个代表节点）
	uniqueNodes := make([]*Node, 0, len(urlToGroup))
	for _, group := range urlToGroup {
		uniqueNodes = append(uniqueNodes, group.nodes[0]) // 取第一个节点作为代表
	}

	duplicateCount := len(urlToGroup) - len(uniqueNodes)
	if len(urlToGroup) > 0 {
		duplicateCount = 0
		for _, group := range urlToGroup {
			duplicateCount += len(group.nodes) - 1
		}
	}

	slog.Info("proxy: global health check started",
		"total_nodes", totalNodes,
		"unique_urls", len(uniqueNodes),
		"duplicates_skipped", duplicateCount,
		"undialable_skipped", skippedUndialable,
		"interval_skipped", skippedByInterval,
		"healthy_to_check", healthyNodes,
		"unhealthy_to_check", unhealthyNodes,
		"forcible", forcible)

	summary := HealthCheckSummary{Total: totalNodes, Skipped: skippedUndialable}

	// 如果没有需要探活的节点，直接返回
	if len(uniqueNodes) == 0 {
		slog.Debug("proxy: no nodes to health check at this time")
		return summary
	}

	// 4. 并发探活去重后的节点
	checkResults := make(map[int]struct {
		ok        bool
		latency   int
		err       error
		checkedAt time.Time
	})

	for res := range m.checker.CheckConcurrent(ctx, uniqueNodes, 16) {
		checkResults[res.NodeID] = struct {
			ok        bool
			latency   int
			err       error
			checkedAt time.Time
		}{
			ok:        res.OK,
			latency:   res.Latency,
			err:       res.Err,
			checkedAt: res.CheckedAt,
		}
		if m.metrics != nil {
			status := "failed"
			if res.OK {
				status = "success"
				m.metrics.ObserveNodeResponseTime(float64(res.Latency))
			}
			m.metrics.IncHealthCheck(status)
			m.metrics.ObserveHealthCheckDuration(float64(res.Latency) / 1000)
		}
	}

	// 5. 将结果分发到所有相同 URL 的节点，并设置下次探活时间
	//
	// 审计修正 (2026-09-09 P0)：每个节点在 nodeProbeLock 内完成
	// "GetNode 新鲜快照 → applyHealthCheckResult → UpdateNode →
	// updateNodeInCache" 全序列。原实现 apply 在锁内但作用于 ListNodes
	// 的旧快照，DB/缓存写入被拆到后面的批量阶段——与并发的
	// HealthCheckNode / swapProbeOne 互相覆盖 ConsecutiveFailures。
	// 合并到锁内一步后不再需要独立的批量落库/写缓存阶段。
	successCount := 0
	failedCount := 0

	for _, group := range urlToGroup {
		// 获取代表节点的探活结果
		representativeNode := group.nodes[0]
		result, ok := checkResults[representativeNode.ID]
		if !ok {
			continue
		}

		// 将结果应用到该 URL 的所有节点
		for _, node := range group.nodes {
			nmu := m.nodeProbeLock(node.ID)
			nmu.Lock()
			fresh, gerr := m.store.GetNode(ctx, node.ID)
			if gerr != nil {
				nmu.Unlock()
				slog.Warn("proxy: global health check: get node failed", "node_id", node.ID, "error", gerr)
				continue
			}
			m.applyHealthCheckResult(fresh, result.ok, result.latency, result.checkedAt)
			if uerr := m.store.UpdateNode(ctx, fresh); uerr != nil {
				slog.Warn("proxy: global health check: update node failed", "node_id", fresh.ID, "error", uerr)
			}
			m.updateNodeInCache(fresh)
			nmu.Unlock()

			if result.ok {
				// 健康节点：10 分钟后再探活。
				if !forcible {
					m.scheduleNextHealthCheck(node.ID, now.Add(10*time.Minute))
				}
				successCount++

			} else {
				// 不健康节点：1 分钟后再探活（加快恢复检测）。
				if !forcible {
					m.scheduleNextHealthCheck(node.ID, now.Add(time.Minute))
				}
				failedCount++
				if m.metrics != nil {
					m.metrics.IncHealthFailure()
					m.metrics.ObserveNodeConsecutiveFailures(fresh.ConsecutiveFailures)
				}

				if len(group.nodes) == 1 {
					// 只在非重复节点时记录详细日志
					slog.Warn("proxy: health check failed",
						"node", node.Name, "error", redactErr(result.err), "latency_ms", result.latency)
				}
			}

			// Keep active swap state authoritative with global health results.
			subID := fresh.SubscriptionID
			m.recordProbeResult(&subID, fresh.ID, result.ok)
		}
	}

	// 6/7. （已合并到步骤 5）批量落库与批量写缓存阶段移除——旧实现把
	// DB/缓存写入拆到锁外的批量阶段，持旧快照覆盖并发写入方的新状态。

	// 8. 更新指标
	if m.metrics != nil {
		dialableCount := 0
		unhealthyCount := 0
		for _, node := range allNodes {
			if node.Dialable() {
				dialableCount++
			}
			if node.Status == "unhealthy" {
				unhealthyCount++
			}
		}
		m.metrics.SetSubscriptionNodeCount(len(allNodes))
		m.metrics.SetNodeCounts(len(allNodes), dialableCount, unhealthyCount)
	}

	summary.OK = successCount
	summary.Failed = failedCount
	if successCount > 0 {
		// Per-representative averaging: each dedup group contributes a single
		// latency sample; dividing by node count would silently halve the value
		// whenever multiple nodes share proxy+health URL.
		perRep := 0
		for _, g := range urlToGroup {
			r, ok := checkResults[g.nodes[0].ID]
			if !ok || !r.ok {
				continue
			}
			summary.AvgMs += r.latency
			if r.latency > summary.MaxMs {
				summary.MaxMs = r.latency
			}
			perRep++
		}
		if perRep > 0 {
			summary.AvgMs /= perRep
		}
	}

	elapsed := time.Since(startTime)
	slog.Info("proxy: global health check completed",
		"total_nodes", totalNodes,
		"checked_urls", len(uniqueNodes),
		"duplicates_saved", duplicateCount,
		"interval_skipped", skippedByInterval,
		"success", successCount,
		"failed", failedCount,
		"elapsed_ms", elapsed.Milliseconds(),
		"forcible", forcible)
	return summary
}

// HealthCheckSummary 一次订阅并发探活的汇总。
type HealthCheckSummary struct {
	Total   int
	OK      int
	Failed  int
	Skipped int // 不可拨号（trojan/vless 等）节点数
	AvgMs   int
	MaxMs   int
}

// HealthCheckSubscription 对单个订阅下的节点做并发探活，并把结果写回 Store 与缓存。
// 相比逐节点串行（每次请求 + 100ms sleep），并发受限于并发度，100+ 节点整体耗时显著下降。
func (m *Manager) HealthCheckSubscription(ctx context.Context, subscriptionID int) (HealthCheckSummary, error) {
	return m.HealthCheckSubscriptionNow(ctx, subscriptionID)
}

// SanitizeSubscriptionError removes the complete subscription URL, including
// path tokens, and then applies the general credential redaction rules.
func SanitizeSubscriptionError(message, subscribeURL string) string {
	if subscribeURL != "" {
		if u, err := url.Parse(subscribeURL); err == nil && u.Scheme != "" && u.Host != "" {
			u.User = nil
			u.Path = "/redacted"
			u.RawPath = ""
			u.RawQuery = ""
			u.ForceQuery = false
			u.Fragment = ""
			u.RawFragment = ""
			redactedURL := u.String()
			message = strings.ReplaceAll(message, subscribeURL, redactedURL)
			message = strings.ReplaceAll(message, url.QueryEscape(subscribeURL), url.QueryEscape(redactedURL))
		}
	}
	return SanitizeSecrets(message)
}

// 审计修复 (2026-08-29)：密钥安全 - 实际调用 sanitizeSecrets 进行脱敏。
// 二次审计修复 (2026-08-29)：调用 parser.SanitizeSecrets 公开包装，覆盖
// key=value / password=xxx / token=xxx 等键值对形式，避免日志里泄露凭据。
func redactErr(err error) string {
	if err == nil {
		return ""
	}
	return SanitizeSecrets(err.Error())
}

func (m *Manager) loadAllNodesIntoCache() error {
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	nodes, err := m.store.ListNodes(ctx, nil)
	if err != nil {
		return err
	}

	// 按订阅 ID 分组
	nodesBySubscription := make(map[int][]*Node)
	for _, node := range nodes {
		nodesBySubscription[node.SubscriptionID] = append(
			nodesBySubscription[node.SubscriptionID], node)
	}

	// 存入缓存（带 TTL）
	now := time.Now()
	for subID, nodes := range nodesBySubscription {
		m.setCacheWithTTL(subID, nodes, now, true)
	}
	// R4 #13 negative-cache 契约：空订阅同样安装 covered 空条目。
	m.installNegativeCacheEntries(ctx, nodesBySubscription)

	// 启动时同步订阅级禁用地区。
	m.refreshAllSubscriptionBans(ctx)
	// 启动时如果 DB 中已有策略，则加载。
	if err := m.LoadSelectionPolicy(ctx); err != nil {
		slog.Warn("proxy: load selection policy on start", "error", err)
	}
	return nil
}

func (m *Manager) loadNodesIntoCache(ctx context.Context, subscriptionID int) bool {
	nodes, err := m.store.ListNodes(ctx, &subscriptionID)
	if err != nil {
		slog.Error("proxy: failed to load nodes into cache", "error", err)
		return false
	}

	// 审计修复 (2026-09-01 P2)：订阅刷新后节点集为空（例如上游已删了节点列表）
	// 时，同步丢弃该订阅的负载均衡游标。否则 roundRobinIndex/weightedCurrent
	// 中该订阅的条目只增不减，长期运行下累积泄漏。
	if len(nodes) == 0 {
		m.forgetLoadBalancerState(subscriptionID)
	}

	// 检查密码解密失败并记录指标
	if m.metrics != nil {
		for _, node := range nodes {
			if node.PasswordDecryptFailed {
				m.metrics.IncPasswordDecryptFailed()
			}
		}
	}

	m.setCacheWithTTL(subscriptionID, nodes, time.Now(), true)
	// 顺手刷新 banned 缓存。
	m.refreshSubscriptionBans(ctx, subscriptionID)
	return true
}

// forgetLoadBalancerState 丢弃一个订阅在负载均衡器中的策略状态（轮询游标与
// smooth-WRR current 表）。2026-09-01 审计 P2 修复：这些 per-subscription map
// 此前只增不减，节点全下线的订阅会永久留下条目。
func (m *Manager) forgetLoadBalancerState(subscriptionID int) {
	m.selectionMu.Lock()
	if m.loadBalancer != nil {
		m.loadBalancer.ForgetSubscription(subscriptionID)
	}
	m.selectionMu.Unlock()
}

// getNodesFromCache 已废弃，使用 getNodesFromCacheWithTTL 替代
func (m *Manager) getNodesFromCache(subscriptionID int) []*Node {
	nodes, _, _, _ := m.getNodesFromCacheWithTTL(subscriptionID, time.Now())
	return nodes
}

// getNodesFromCacheWithTTL returns an isolated snapshot, freshness, cache
// presence, and coverage. Expired snapshots remain available for this request
// while one asynchronous refresh is in flight, so callers neither block nor
// create a database thundering herd.
func (m *Manager) getNodesFromCacheWithTTL(subscriptionID int, now time.Time) ([]*Node, bool, bool, bool) {
	mu := m.getCacheLock(subscriptionID)
	mu.RLock()
	defer mu.RUnlock()
	value, ok := m.nodesCache.Load(subscriptionID)
	if !ok {
		return nil, false, false, false
	}
	entry := value.(*cacheEntry)
	fresh := now.Before(entry.expiresAt)
	if !fresh {
		m.refreshCacheAsync(subscriptionID)
	}
	return cloneNodes(entry.nodes), fresh, true, entry.covered
}

func (m *Manager) refreshCacheAsync(subscriptionID int) {
	// 串行化 wg.Add(1) 与 Stop() 的 wg.Wait()：Stop 调用前会持有 lifecycleMu，
	// 这里同样在锁内做 LoadOrStore + wg.Add(1)，避免请求路径上的 Add 晚于
	// Stop 内部的 Wait 导致 WaitGroup 进入未定义状态。
	m.lifecycleMu.Lock()
	if m.stopped {
		m.lifecycleMu.Unlock()
		return
	}
	if _, loaded := m.cacheRefreshes.LoadOrStore(subscriptionID, struct{}{}); loaded {
		m.lifecycleMu.Unlock()
		return
	}
	m.wg.Add(1)
	m.lifecycleMu.Unlock()
	go func() {
		defer m.wg.Done()
		defer m.cacheRefreshes.Delete(subscriptionID)
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		m.loadNodesIntoCache(ctx, subscriptionID)
	}()
}

// installNegativeCacheEntries 落实 R4 #13 negative-cache 契约：全量加载按
// 节点分组时，"store 中存在但没有任何节点"的订阅不会自然产生缓存条目，
// 该订阅的 targeted selection 在首个请求前会每次回源 ListNodes。本函数为
// 这些订阅安装 present-but-empty 的 covered 条目（空节点集是合法的完整
// 快照），并返回当前 store 的全部订阅 ID，供 ReloadCache 的孤儿清扫豁免
// 这些条目。ListSubscriptions 失败时返回 nil（降级为不安装、清扫不豁免）。
func (m *Manager) installNegativeCacheEntries(ctx context.Context, nodesBySubscription map[int][]*Node) map[int]struct{} {
	subs, err := m.store.ListSubscriptions(ctx)
	if err != nil {
		slog.Warn("proxy: install negative cache entries: list subscriptions failed", "error", err)
		return nil
	}
	known := make(map[int]struct{}, len(subs))
	now := time.Now()
	for _, sub := range subs {
		if sub == nil {
			continue
		}
		known[sub.ID] = struct{}{}
		if _, hasNodes := nodesBySubscription[sub.ID]; hasNodes {
			continue
		}
		m.setCacheWithTTL(sub.ID, []*Node{}, now, true)
	}
	return known
}

// setCacheWithTTL 设置缓存，带 TTL（阶段 2 优化）。covered 必须反映 nodes
// 是否为该订阅在 store.ListNodes 下的完整快照；当前全部写入点都来自
// ListNodes，因此传 true。
func (m *Manager) setCacheWithTTL(subscriptionID int, nodes []*Node, now time.Time, covered bool) {
	mu := m.getCacheLock(subscriptionID)
	mu.Lock()
	defer mu.Unlock()
	entry := &cacheEntry{
		nodes:     cloneNodes(nodes),
		expiresAt: now.Add(m.cacheTTL),
		covered:   covered,
	}
	m.nodesCache.Store(subscriptionID, entry)
}

func (m *Manager) getAllActiveCachedNodes(now time.Time) ([]*Node, bool) {
	var allNodes []*Node
	cachePresent := false
	m.nodesCache.Range(func(key, value interface{}) bool {
		subscriptionID, ok := key.(int)
		if !ok {
			return true
		}
		cachePresent = true
		entry := value.(*cacheEntry)
		if !now.Before(entry.expiresAt) {
			m.refreshCacheAsync(subscriptionID)
		}
		allNodes = append(allNodes, cloneNodes(entry.nodes)...)
		return true
	})
	return allNodes, cachePresent
}

// cloneNodes returns a snapshot that shares no mutable Node or Config state
// with the caller. Cache readers can therefore sort/filter while health checks
// update their store-owned nodes concurrently.
func cloneNodes(nodes []*Node) []*Node {
	if nodes == nil {
		return nil
	}
	cloned := make([]*Node, len(nodes))
	for i, node := range nodes {
		cloned[i] = cloneNode(node)
	}
	return cloned
}

func cloneNode(node *Node) *Node {
	if node == nil {
		return nil
	}
	cloned := *node
	if node.BannedRegions != nil {
		cloned.BannedRegions = append([]string(nil), node.BannedRegions...)
	}
	cloned.Config = cloneConfig(node.Config)
	return &cloned
}

func cloneConfig(config map[string]interface{}) map[string]interface{} {
	if config == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(config))
	for key, value := range config {
		cloned[key] = cloneConfigValue(value)
	}
	return cloned
}

func cloneConfigValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneConfig(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for i, item := range typed {
			cloned[i] = cloneConfigValue(item)
		}
		return cloned
	default:
		return value
	}
}

// 审计修复 (2026-08-29)：问题 6 - 使用深拷贝避免 slice 竞态条件。
func (m *Manager) updateNodeInCache(node *Node) {
	// 审计修复 (2026-08-29)：并发安全 P1-2 - 使用 per-subscription 锁保护并发写入
	mu := m.getCacheLock(node.SubscriptionID)
	mu.Lock()
	defer mu.Unlock()

	value, ok := m.nodesCache.Load(node.SubscriptionID)
	if !ok {
		return
	}
	entry := value.(*cacheEntry)

	// 深拷贝 slice，避免并发读写竞态
	newNodes := make([]*Node, len(entry.nodes))
	copy(newNodes, entry.nodes)

	// 更新缓存中的节点
	for i, n := range newNodes {
		if n != nil && n.ID == node.ID {
			newNodes[i] = cloneNode(node)
			break
		}
	}

	// 保持原有的过期时间（阶段 2 优化：TTL），在同一锁内原子替换。
	m.nodesCache.Store(node.SubscriptionID, &cacheEntry{
		nodes:     newNodes,
		expiresAt: entry.expiresAt,
	})
}

// getCacheLock 获取 subscription 的锁（lazy initialization）
// 审计修复 (2026-08-29)：并发安全 P1-2 - per-subscription 锁
func (m *Manager) getCacheLock(subscriptionID int) *sync.RWMutex {
	v, _ := m.cacheLocks.LoadOrStore(subscriptionID, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

// nodeProbeLock 取得该节点 ID 的互斥锁。HealthCheckNode / swapProbeOne / 批量
// 探活的 per-node 分支都应在进入探活前 Lock、写回后 Unlock，避免并发读写 *Node。
func (m *Manager) nodeProbeLock(nodeID int) *sync.Mutex {
	v, _ := m.nodeProbes.LoadOrStore(nodeID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (m *Manager) nextHealthCheckAt(nodeID int) (time.Time, bool) {
	value, ok := m.nextHealthChecks.Load(nodeID)
	if !ok {
		return time.Time{}, false
	}
	next, ok := value.(time.Time)
	return next, ok
}

func (m *Manager) scheduleNextHealthCheck(nodeID int, next time.Time) {
	m.nextHealthChecks.Store(nodeID, next)
}

// extractDomain 从 URL 提取域名
func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// RegionStats 按地区汇总的可观测统计。
type RegionStats struct {
	Region        string `json:"region"`
	Total         int    `json:"total"`
	Dialable      int    `json:"dialable"`
	Active        int    `json:"active"`
	Unhealthy     int    `json:"unhealthy"`
	Banned        int    `json:"banned"`         // 当前被订阅+节点禁用地区命中的节点数
	AvgLatencyMs  int    `json:"avg_latency_ms"` // 成功探活的平均响应时间（毫秒）
	BestLatencyMs int    `json:"best_latency_ms"`
	BestNodeID    int    `json:"best_node_id"`
	BestNodeName  string `json:"best_node_name"`
}

// RegionStatsReport 返回按地区分组的节点健康快照，供 admin UI 的"地区分布"视图。
func (m *Manager) RegionStatsReport(ctx context.Context) ([]RegionStats, error) {
	nodes, err := m.store.ListNodes(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	m.selectionMu.Lock()
	bansSnapshot := m.subscriptionBansSnapshot(nil)
	m.selectionMu.Unlock()

	type bucket struct {
		stats RegionStats
	}
	buckets := make(map[string]*bucket)
	order := make([]string, 0)
	for _, n := range nodes {
		if n == nil {
			continue
		}
		// 分桶用 normalizeRegion 口径，"US" 与 "us " 归入同一地区。
		region := normalizeRegion(n.Location)
		if region == "" {
			region = "(unknown)"
		}
		b, ok := buckets[region]
		if !ok {
			b = &bucket{stats: RegionStats{Region: region}}
			buckets[region] = b
			order = append(order, region)
		}
		b.stats.Total++
		if n.Dialable() {
			b.stats.Dialable++
		}
		switch n.Status {
		case "active":
			b.stats.Active++
		case "unhealthy":
			b.stats.Unhealthy++
		}
		if IsRegionBanned(n.Location, bansSnapshot[n.SubscriptionID], n.BannedRegions) {
			b.stats.Banned++
		}
		if n.LastHealthCheckStatus == "success" && n.ResponseTimeMs > 0 {
			if b.stats.BestLatencyMs == 0 || n.ResponseTimeMs < b.stats.BestLatencyMs {
				b.stats.BestLatencyMs = n.ResponseTimeMs
				b.stats.BestNodeID = n.ID
				b.stats.BestNodeName = n.Name
			}
		}
	}
	// 计算平均。
	for _, region := range order {
		b := buckets[region]
		var sum, count int
		for _, n := range nodes {
			if n == nil {
				continue
			}
			eff := normalizeRegion(n.Location)
			if eff == "" {
				eff = "(unknown)"
			}
			if eff != region {
				continue
			}
			if n.LastHealthCheckStatus == "success" && n.ResponseTimeMs > 0 {
				sum += n.ResponseTimeMs
				count++
			}
		}
		if count > 0 {
			b.stats.AvgLatencyMs = sum / count
		}
	}
	out := make([]RegionStats, 0, len(order))
	for _, region := range order {
		out = append(out, buckets[region].stats)
	}
	return out, nil
}
