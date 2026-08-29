package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// cacheEntry 缓存条目，包含节点列表和过期时间（阶段 2 优化：TTL 机制）
type cacheEntry struct {
	nodes     []*Node
	expiresAt time.Time
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

	// 内存缓存：subscription_id -> cacheEntry（阶段 2 优化：从 []*Node 改为带 TTL 的 cacheEntry）
	nodesCache sync.Map
	// cacheLocks protects copy-on-write updates for one subscription.
	cacheLocks sync.Map // subscription_id -> *sync.RWMutex
	// cacheRefreshes suppresses duplicate asynchronous refreshes for expired entries.
	cacheRefreshes sync.Map // subscription_id -> struct{}
	// nextHealthChecks is process-local scheduling state. It is intentionally not
	// persisted: after restart, nodes are safely eligible for a fresh probe.
	nextHealthChecks sync.Map // node_id -> time.Time

	// 配置
	autoRefreshInterval   time.Duration
	healthCheckInterval   time.Duration
	unknownDomainStrategy string        // direct/proxy/probe
	cacheTTL              time.Duration // 缓存 TTL，默认 5 分钟
	autoDisableThreshold  int
	autoDisableEnabled    bool
	autoRecoverEnabled    bool

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
	defer m.lifecycleMu.Unlock()
	if m.started || m.stopped {
		return
	}
	m.started = true

	if err := m.loadAllNodesIntoCache(); err != nil {
		slog.Error("proxy: failed to load nodes into cache", "error", err)
	}
	// Keep lifecycleMu while adding workers so Stop cannot call Wait concurrently
	// with WaitGroup.Add.
	m.wg.Add(2)
	go m.refreshLoop()
	go m.healthCheckLoop()
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

func (m *Manager) selectNode(ctx context.Context, subscriptionID *int, requestKey, preferredLocation string, bestOnly bool) (_ *Node, err error) {
	startedAt := time.Now()
	result := "store_error"
	defer func() {
		if m.metrics != nil {
			m.metrics.ObserveNodeSelection(result, time.Since(startedAt).Seconds())
		}
	}()

	var candidates []*Node
	if subscriptionID != nil {
		candidates, _ = m.getNodesFromCacheWithTTL(*subscriptionID, time.Now())
	} else {
		candidates = m.getAllActiveCachedNodes(time.Now())
	}
	if len(candidates) == 0 {
		candidates, err = m.store.ListNodes(ctx, subscriptionID)
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}
		if subscriptionID != nil {
			m.setCacheWithTTL(*subscriptionID, candidates, time.Now())
		}
	}

	m.selectionMu.Lock()
	defer m.selectionMu.Unlock()
	threshold := m.autoDisableThreshold
	if threshold <= 0 {
		threshold = 3
	}
	activeNodes := make([]*Node, 0, len(candidates))
	undialable := 0
	for _, node := range candidates {
		if node == nil || node.Status != "active" || node.PasswordDecryptFailed || node.ConsecutiveFailures >= threshold {
			continue
		}
		if !node.Dialable() {
			undialable++
			continue
		}
		activeNodes = append(activeNodes, node)
	}
	if len(activeNodes) == 0 {
		if undialable > 0 {
			result = "no_dialable"
			if m.metrics != nil {
				m.metrics.IncEgressSelection("undialable")
			}
			return nil, fmt.Errorf("no dialable proxy node: %d node(s) use protocols Go cannot proxy directly (trojan/vless/vmess/ss); expose them via a local mihomo/xray http or socks5 bridge and register that endpoint instead", undialable)
		}
		result = "no_available"
		if m.metrics != nil {
			m.metrics.IncEgressSelection("none")
		}
		return nil, fmt.Errorf("no available active nodes")
	}

	sort.Slice(activeNodes, func(i, j int) bool {
		if activeNodes[i].ConsecutiveFailures != activeNodes[j].ConsecutiveFailures {
			return activeNodes[i].ConsecutiveFailures < activeNodes[j].ConsecutiveFailures
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
	return selected, nil
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
		sub.LastError = err.Error()
		_ = m.store.UpdateSubscription(ctx, sub)

		// 记录失败指标
		if m.metrics != nil {
			m.metrics.ObserveSubscriptionRefresh("failed", time.Since(startTime).Seconds())
		}
		return fmt.Errorf("parse subscription: %w", err)
	}

	// 3. 更新数据库（事务保护，审计修复 2026-08-29 问题 1）
	for _, node := range nodes {
		node.SubscriptionID = subscriptionID
	}

	// 使用事务方法原子性地删除旧节点并插入新节点，避免中断导致订阅变空。
	if pgStore, ok := m.store.(*PgStore); ok {
		if err := pgStore.RefreshSubscriptionNodes(ctx, subscriptionID, nodes); err != nil {
			sub.NodeCount = 0
			sub.LastFetchAt = time.Now()
			sub.LastFetchStatus = "failed"
			sub.LastError = fmt.Sprintf("transaction failed: %v", err)
			_ = m.store.UpdateSubscription(ctx, sub)

			// 记录失败指标
			if m.metrics != nil {
				m.metrics.ObserveSubscriptionRefresh("failed", time.Since(startTime).Seconds())
			}
			return fmt.Errorf("refresh nodes in transaction: %w", err)
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
			sub.LastError = fmt.Sprintf("persisted %d/%d nodes; %d failed: %s",
				len(nodes)-len(createErrs), len(nodes), len(createErrs), strings.Join(createErrs, "; "))
			_ = m.store.UpdateSubscription(ctx, sub)

			// 记录失败指标
			if m.metrics != nil {
				m.metrics.ObserveSubscriptionRefresh("failed", time.Since(startTime).Seconds())
			}
			return fmt.Errorf("persist nodes: %d/%d failed: %s",
				len(createErrs), len(nodes), strings.Join(createErrs, "; "))
		}
	}

	// 4. 更新订阅状态
	sub.NodeCount = len(nodes)
	sub.LastFetchAt = time.Now()
	sub.LastFetchStatus = "success"
	sub.LastError = ""
	if err := m.store.UpdateSubscription(ctx, sub); err != nil {
		return fmt.Errorf("update subscription: %w", err)
	}

	// 5. 刷新内存缓存
	m.loadNodesIntoCache(ctx, subscriptionID)

	// 记录成功指标
	if m.metrics != nil {
		m.metrics.ObserveSubscriptionRefresh("success", time.Since(startTime).Seconds())
	}

	slog.Info("proxy: subscription refreshed", "id", subscriptionID, "node_count", len(nodes))
	return nil
}

// HealthCheckNode 健康检查单个节点
func (m *Manager) HealthCheckNode(ctx context.Context, nodeID int) error {
	node, err := m.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node: %w", err)
	}
	if node.PasswordDecryptFailed {
		if m.metrics != nil {
			m.metrics.IncPasswordDecryptFailed()
		}
		return fmt.Errorf("health check node %d: password decryption failed", nodeID)
	}

	startTime := time.Now()
	responseTimeMs, err := m.checker.Check(ctx, node)
	elapsed := time.Since(startTime)

	node.LastHealthCheckAt = time.Now()

	if err != nil {
		node.LastHealthCheckStatus = "failed"
		node.ConsecutiveFailures++

		if node.ConsecutiveFailures >= m.autoDisableThreshold {
			node.Status = "unhealthy"
		}

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
		node.LastHealthCheckStatus = "success"
		node.ResponseTimeMs = responseTimeMs

		node.ConsecutiveFailures = 0
		node.Status = "active"

		// 更新成功率（简单的移动平均）
		node.SuccessRate = node.SuccessRate*0.9 + 0.1

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

	return nil
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
	m.nodesCache.Range(func(key, _ interface{}) bool {
		subID := key.(int)
		if _, exists := nodesBySubscription[subID]; !exists {
			m.nodesCache.Delete(subID)
		}
		return true
	})

	now := time.Now()
	for subID, nodes := range nodesBySubscription {
		m.setCacheWithTTL(subID, nodes, now)
	}

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

	proxyURLStr := node.ProxyURL()
	if proxyURLStr == "" {
		return nil, fmt.Errorf("unsupported proxy protocol: %s", node.Protocol)
	}

	subID := 0
	if subscriptionID != nil {
		subID = *subscriptionID
	}
	// 工厂按订阅缓存 Transport，仅当选出的节点代理 URL 变化时才重建；
	// 命中同一订阅的多次请求共享连接池。
	return m.transportFactory.Get(subID, proxyURLStr)
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
			m.healthCheckAllNodes(ctx)
			cancel()
		case <-m.stopCh:
			return
		case <-m.ctx.Done():
			return
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
	startTime := time.Now()
	now := time.Now()

	// 阶段 2 优化：全局批量探活，按 URL 去重 + 智能探活间隔
	// 1. 收集所有活跃订阅的节点
	allNodes, err := m.store.ListNodes(ctx, nil)
	if err != nil {
		slog.Error("proxy: failed to list all nodes for health check", "error", err)
		return
	}

	// 2. 过滤需要探活的节点（基于智能间隔），并按 ProxyURL 分组去重
	type nodeGroup struct {
		proxyURL string
		nodes    []*Node // 相同 URL 的所有节点
	}
	urlToGroup := make(map[string]*nodeGroup)
	totalNodes := 0
	skippedUndialable := 0
	skippedByInterval := 0 // 由于智能间隔跳过的节点
	healthyNodes := 0
	unhealthyNodes := 0

	for _, node := range allNodes {
		// 只检查 active 和 unhealthy 状态的节点
		if node.Status != "active" && node.Status != "unhealthy" {
			continue
		}
		totalNodes++

		// 跳过不可拨号的节点
		if !node.Dialable() {
			skippedUndialable++
			continue
		}

		if next, ok := m.nextHealthCheckAt(node.ID); ok && now.Before(next) {
			skippedByInterval++
			continue
		}

		proxyURL := node.ProxyURL()
		if proxyURL == "" {
			continue
		}

		// 按 URL 分组
		if _, exists := urlToGroup[proxyURL]; !exists {
			urlToGroup[proxyURL] = &nodeGroup{
				proxyURL: proxyURL,
				nodes:    []*Node{node},
			}
		} else {
			urlToGroup[proxyURL].nodes = append(urlToGroup[proxyURL].nodes, node)
		}

		// 统计健康/不健康节点数
		if node.ConsecutiveFailures == 0 {
			healthyNodes++
		} else {
			unhealthyNodes++
		}
	}

	// 3. 构建去重后的节点列表（每个 URL 只取一个代表节点）
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
		"unhealthy_to_check", unhealthyNodes)

	// 如果没有需要探活的节点，直接返回
	if len(uniqueNodes) == 0 {
		slog.Debug("proxy: no nodes to health check at this time")
		return
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
	nodesToUpdate := make([]*Node, 0, len(uniqueNodes)*2)
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
			node.LastHealthCheckAt = result.checkedAt

			if result.ok {
				node.LastHealthCheckStatus = "success"
				node.ResponseTimeMs = result.latency

				node.ConsecutiveFailures = 0
				node.Status = "active"
				node.SuccessRate = node.SuccessRate*0.9 + 0.1

				// 健康节点：10 分钟后再探活。
				m.scheduleNextHealthCheck(node.ID, now.Add(10*time.Minute))
				successCount++

			} else {
				node.LastHealthCheckStatus = "failed"
				node.ConsecutiveFailures++

				if node.ConsecutiveFailures >= 3 {
					node.Status = "unhealthy"
				}

				// 不健康节点：1 分钟后再探活（加快恢复检测）。
				m.scheduleNextHealthCheck(node.ID, now.Add(time.Minute))
				failedCount++
				if m.metrics != nil {
					m.metrics.IncHealthFailure()
					m.metrics.ObserveNodeConsecutiveFailures(node.ConsecutiveFailures)
				}

				if len(group.nodes) == 1 {
					// 只在非重复节点时记录详细日志
					slog.Warn("proxy: health check failed",
						"node", node.Name, "error", redactErr(result.err), "latency_ms", result.latency)
				}
			}

			nodesToUpdate = append(nodesToUpdate, node)
		}
	}

	// 6. 批量更新数据库（分批，每批 50 个节点）
	batchSize := 50
	for i := 0; i < len(nodesToUpdate); i += batchSize {
		end := i + batchSize
		if end > len(nodesToUpdate) {
			end = len(nodesToUpdate)
		}
		batch := nodesToUpdate[i:end]

		// 使用事务批量更新
		if pgStore, ok := m.store.(*PgStore); ok {
			if err := pgStore.BatchUpdateNodes(ctx, batch); err != nil {
				slog.Error("proxy: batch update nodes failed", "error", err, "batch_size", len(batch))
				// 降级到逐个更新
				for _, node := range batch {
					if uerr := m.store.UpdateNode(ctx, node); uerr != nil {
						slog.Warn("proxy: update node failed", "node_id", node.ID, "error", uerr)
					}
				}
			}
		} else {
			// 非 PgStore 实现，逐个更新
			for _, node := range batch {
				if uerr := m.store.UpdateNode(ctx, node); uerr != nil {
					slog.Warn("proxy: update node failed", "node_id", node.ID, "error", uerr)
				}
			}
		}
	}

	// 7. 批量更新缓存
	for _, node := range nodesToUpdate {
		m.updateNodeInCache(node)
	}

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

	elapsed := time.Since(startTime)
	slog.Info("proxy: global health check completed",
		"total_nodes", totalNodes,
		"checked_urls", len(uniqueNodes),
		"duplicates_saved", duplicateCount,
		"interval_skipped", skippedByInterval,
		"success", successCount,
		"failed", failedCount,
		"elapsed_ms", elapsed.Milliseconds())
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
	var summary HealthCheckSummary
	nodes, err := m.store.ListNodes(ctx, &subscriptionID)
	if err != nil {
		return summary, fmt.Errorf("list nodes: %w", err)
	}

	var toCheck []*Node
	for _, n := range nodes {
		if n.Status != "active" && n.Status != "unhealthy" {
			continue
		}
		summary.Total++
		if !n.Dialable() {
			summary.Skipped++
			continue
		}
		toCheck = append(toCheck, n)
	}

	for res := range m.checker.CheckConcurrent(ctx, toCheck, 16) {
		node, gerr := m.store.GetNode(ctx, res.NodeID)
		if gerr != nil {
			slog.Warn("proxy: health check: get node failed", "node_id", res.NodeID, "error", gerr)
			continue
		}
		node.LastHealthCheckAt = res.CheckedAt
		if m.metrics != nil {
			status := "failed"
			if res.OK {
				status = "success"
				m.metrics.ObserveNodeResponseTime(float64(res.Latency))
			}
			m.metrics.IncHealthCheck(status)
			m.metrics.ObserveHealthCheckDuration(float64(res.Latency) / 1000)
		}
		if res.OK {
			node.LastHealthCheckStatus = "success"
			node.ResponseTimeMs = res.Latency
			node.ConsecutiveFailures = 0
			node.Status = "active"
			node.SuccessRate = node.SuccessRate*0.9 + 0.1
			summary.OK++
			summary.AvgMs += res.Latency
			if res.Latency > summary.MaxMs {
				summary.MaxMs = res.Latency
			}
		} else {
			node.LastHealthCheckStatus = "failed"
			node.ConsecutiveFailures++
			if node.ConsecutiveFailures >= 3 {
				node.Status = "unhealthy"
			}
			summary.Failed++

			if m.metrics != nil {
				m.metrics.ObserveNodeConsecutiveFailures(node.ConsecutiveFailures)
			}

			slog.Warn("proxy: health check failed",
				"node", node.Name, "error", redactErr(res.Err), "latency_ms", res.Latency)
		}
		if uerr := m.store.UpdateNode(ctx, node); uerr != nil {
			slog.Warn("proxy: health check: update node failed", "node_id", node.ID, "error", uerr)
			continue
		}
		m.updateNodeInCache(node)
	}
	if summary.OK > 0 {
		summary.AvgMs /= summary.OK
	}
	// 指标：探活失败累计 + 节点计数（可拨号 = 总数 - 不可拨号跳过项）。
	if m.metrics != nil {
		for i := 0; i < summary.Failed; i++ {
			m.metrics.IncHealthFailure()
		}
		dialable := summary.Total - summary.Skipped
		if dialable < 0 {
			dialable = 0
		}
		m.metrics.SetNodeCounts(summary.Total, dialable, summary.Total-summary.OK-summary.Skipped)
	}
	return summary, nil
}

// redactErr 去掉错误里可能泄露的代理凭据（ProxyURL 出现在错误信息中）。
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

	// 存入缓存（带 TTL）
	now := time.Now()
	for subID, nodes := range nodesBySubscription {
		m.setCacheWithTTL(subID, nodes, now)
	}

	return nil
}

func (m *Manager) loadNodesIntoCache(ctx context.Context, subscriptionID int) {
	nodes, err := m.store.ListNodes(ctx, &subscriptionID)
	if err != nil {
		slog.Error("proxy: failed to load nodes into cache", "error", err)
		return
	}

	// 检查密码解密失败并记录指标
	if m.metrics != nil {
		for _, node := range nodes {
			if node.PasswordDecryptFailed {
				m.metrics.IncPasswordDecryptFailed()
			}
		}
	}

	m.setCacheWithTTL(subscriptionID, nodes, time.Now())
}

// getNodesFromCache 已废弃，使用 getNodesFromCacheWithTTL 替代
func (m *Manager) getNodesFromCache(subscriptionID int) []*Node {
	nodes, _ := m.getNodesFromCacheWithTTL(subscriptionID, time.Now())
	return nodes
}

// getNodesFromCacheWithTTL returns a snapshot and whether it is still fresh.
// Expired snapshots remain available for this request while one asynchronous
// refresh is in flight, so callers neither block nor create a database thundering herd.
func (m *Manager) getNodesFromCacheWithTTL(subscriptionID int, now time.Time) ([]*Node, bool) {
	value, ok := m.nodesCache.Load(subscriptionID)
	if !ok {
		return nil, false
	}
	entry := value.(*cacheEntry)
	if now.Before(entry.expiresAt) {
		return entry.nodes, true
	}
	m.refreshCacheAsync(subscriptionID)
	return entry.nodes, false
}

func (m *Manager) refreshCacheAsync(subscriptionID int) {
	if _, loaded := m.cacheRefreshes.LoadOrStore(subscriptionID, struct{}{}); loaded {
		return
	}
	go func() {
		defer m.cacheRefreshes.Delete(subscriptionID)
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		m.loadNodesIntoCache(ctx, subscriptionID)
	}()
}

// setCacheWithTTL 设置缓存，带 TTL（阶段 2 优化）
func (m *Manager) setCacheWithTTL(subscriptionID int, nodes []*Node, now time.Time) {
	entry := &cacheEntry{
		nodes:     nodes,
		expiresAt: now.Add(m.cacheTTL),
	}
	m.nodesCache.Store(subscriptionID, entry)
}

func (m *Manager) getAllActiveCachedNodes(now time.Time) []*Node {
	var allNodes []*Node
	m.nodesCache.Range(func(key, value interface{}) bool {
		subscriptionID, ok := key.(int)
		if !ok {
			return true
		}
		entry := value.(*cacheEntry)
		if !now.Before(entry.expiresAt) {
			m.refreshCacheAsync(subscriptionID)
		}
		allNodes = append(allNodes, entry.nodes...)
		return true
	})
	return allNodes
}

// 审计修复 (2026-08-29)：问题 6 - 使用深拷贝避免 slice 竞态条件。
func (m *Manager) updateNodeInCache(node *Node) {
	// 审计修复 (2026-08-29)：并发安全 P1-2 - 使用 per-subscription 锁保护并发写入
	mu := m.getCacheLock(node.SubscriptionID)
	mu.Lock()
	defer mu.Unlock()

	nodes := m.getNodesFromCache(node.SubscriptionID)
	if nodes == nil {
		return
	}

	// 深拷贝 slice，避免并发读写竞态
	newNodes := make([]*Node, len(nodes))
	copy(newNodes, nodes)

	// 更新缓存中的节点
	for i, n := range newNodes {
		if n.ID == node.ID {
			newNodes[i] = node
			break
		}
	}

	// 保持原有的过期时间（阶段 2 优化：TTL）
	if value, ok := m.nodesCache.Load(node.SubscriptionID); ok {
		entry := value.(*cacheEntry)
		m.nodesCache.Store(node.SubscriptionID, &cacheEntry{
			nodes:     newNodes,
			expiresAt: entry.expiresAt, // 保持原有过期时间
		})
	} else {
		// 如果缓存不存在，设置新的过期时间
		m.setCacheWithTTL(node.SubscriptionID, newNodes, time.Now())
	}
}

// getCacheLock 获取 subscription 的锁（lazy initialization）
// 审计修复 (2026-08-29)：并发安全 P1-2 - per-subscription 锁
func (m *Manager) getCacheLock(subscriptionID int) *sync.RWMutex {
	v, _ := m.cacheLocks.LoadOrStore(subscriptionID, &sync.RWMutex{})
	return v.(*sync.RWMutex)
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
