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
	// transportFactory 按订阅缓存可复用的 http.Transport（Stage 2：避免每次
	// SelectBestNode/探活都新建 Transport 导致连接泄漏）。
	transportFactory *TransportFactory
	// metrics 代理子系统 Prometheus 指标。
	metrics *Metrics

	// 内存缓存：subscription_id -> cacheEntry（阶段 2 优化：从 []*Node 改为带 TTL 的 cacheEntry）
	nodesCache sync.Map
	// 审计修复 (2026-08-29)：并发安全 P1-2 - per-subscription 锁保护并发写入
	cacheLocks sync.Map // subscription_id -> *sync.RWMutex

	// 配置
	autoRefreshInterval   time.Duration
	healthCheckInterval   time.Duration
	unknownDomainStrategy string // direct/proxy/probe
	cacheTTL              time.Duration // 缓存 TTL，默认 5 分钟

	// 审计修复 (2026-08-29)：问题 7 - 增加 context 用于优雅停止后台 goroutine。
	ctx    context.Context
	cancel context.CancelFunc
	stopCh chan struct{}
	wg     sync.WaitGroup
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
		transportFactory:      factory,
		metrics:               metrics,
		autoRefreshInterval:   time.Hour,
		healthCheckInterval:   5 * time.Minute,
		unknownDomainStrategy: "direct",
		cacheTTL:              5 * time.Minute,
		ctx:                   ctx,
		cancel:                cancel,
		stopCh:                make(chan struct{}),
	}
}

// Start 启动定时任务
func (m *Manager) Start() {
	// 启动时立即加载所有节点到内存
	if err := m.loadAllNodesIntoCache(); err != nil {
		slog.Error("proxy: failed to load nodes into cache", "error", err)
	}
	
	// 定时刷新订阅
	m.wg.Add(1)
	go m.refreshLoop()
	
	// 定时健康检查
	m.wg.Add(1)
	go m.healthCheckLoop()
}

// Stop 停止定时任务，并关闭 Transport 工厂的空闲连接。
// 审计修复 (2026-08-29)：问题 7 - 使用 context 取消和 WaitGroup 确保 goroutine 优雅退出。
func (m *Manager) Stop() {
	m.cancel() // 取消 context，通知所有后台任务停止
	close(m.stopCh)
	m.wg.Wait() // 等待所有后台 goroutine 退出
	if m.transportFactory != nil {
		m.transportFactory.CloseIdleConnections()
	}
}

// SelectBestNode 选择当前最健康、成功率最高且响应最快的可拨号节点。
func (m *Manager) SelectBestNode(ctx context.Context, subscriptionID *int) (_ *Node, err error) {
	startedAt := time.Now()
	result := "store_error"
	defer func() {
		if m.metrics != nil {
			m.metrics.ObserveNodeSelection(result, time.Since(startedAt).Seconds())
		}
	}()

	var candidates []*Node
	if subscriptionID != nil {
		if cached, ok := m.getNodesFromCacheWithTTL(*subscriptionID, time.Now()); ok {
			candidates = cached
		}
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

	activeNodes := make([]*Node, 0, len(candidates))
	undialable := 0
	for _, node := range candidates {
		if node.Status != "active" || node.ConsecutiveFailures >= 3 {
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
	result = "success"
	if m.metrics != nil {
		m.metrics.IncEgressSelection("dialable")
	}
	return activeNodes[0], nil
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
			m.metrics.ObserveSubscriptionRefresh(fmt.Sprintf("%d", subscriptionID), "failed", time.Since(startTime).Seconds())
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
					m.metrics.ObserveSubscriptionRefresh(fmt.Sprintf("%d", subscriptionID), "failed", time.Since(startTime).Seconds())
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
					m.metrics.ObserveSubscriptionRefresh(fmt.Sprintf("%d", subscriptionID), "failed", time.Since(startTime).Seconds())
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
		m.metrics.ObserveSubscriptionRefresh(fmt.Sprintf("%d", subscriptionID), "success", time.Since(startTime).Seconds())
		m.metrics.SetSubscriptionNodeCount(fmt.Sprintf("%d", subscriptionID), len(nodes))
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
	
	startTime := time.Now()
	responseTimeMs, err := m.checker.Check(ctx, node)
	elapsed := time.Since(startTime)
	
	node.LastHealthCheckAt = time.Now()
	
	if err != nil {
		node.LastHealthCheckStatus = "failed"
		node.ConsecutiveFailures++
		
		if node.ConsecutiveFailures >= 3 {
			node.Status = "unhealthy"
		}
		
		// 记录失败指标
		if m.metrics != nil {
			m.metrics.IncHealthCheck("failed")
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
		
		// 阶段 3 优化：自动恢复策略
		if m.autoRecoverEnabled && node.ConsecutiveFailures > 0 {
			slog.Info("proxy: node auto-recovered",
				"node", node.Name,
				"previous_failures", node.ConsecutiveFailures)
		}
		
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
		
		// 智能探活间隔：根据节点健康状态决定是否需要探活
		if !node.NextHealthCheckAt.IsZero() && now.Before(node.NextHealthCheckAt) {
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
		ok      bool
		latency int
		err     error
		checkedAt time.Time
	})
	
	for res := range m.checker.CheckConcurrent(ctx, uniqueNodes, 16) {
		checkResults[res.NodeID] = struct {
			ok      bool
			latency int
			err     error
			checkedAt time.Time
		}{
			ok:      res.OK,
			latency: res.Latency,
			err:     res.Err,
			checkedAt: res.CheckedAt,
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
				
				// 阶段 3 优化：自动恢复策略
				wasUnhealthy := node.ConsecutiveFailures > 0
				node.ConsecutiveFailures = 0
				node.Status = "active"
				node.SuccessRate = node.SuccessRate*0.9 + 0.1
				
				if m.autoRecoverEnabled && wasUnhealthy {
					slog.Info("proxy: node auto-recovered",
						"node", node.Name,
						"url", group.proxyURL)
				}
				
				// 健康节点：10 分钟后再探活
				node.NextHealthCheckAt = now.Add(10 * time.Minute)
				successCount++
				
				// 记录成功指标
				if m.metrics != nil {
					m.metrics.IncHealthCheck("success")
					m.metrics.ObserveNodeResponseTime(float64(result.latency))
				}
			} else {
				node.LastHealthCheckStatus = "failed"
				node.ConsecutiveFailures++
				
				// 阶段 3 优化：使用可配置的阈值自动禁用
				if m.autoDisableEnabled && node.ConsecutiveFailures >= m.maxConsecutiveFailures {
					node.Status = "unhealthy"
					if len(group.nodes) == 1 {
						slog.Warn("proxy: node auto-disabled due to consecutive failures",
							"node", node.Name,
							"consecutive_failures", node.ConsecutiveFailures,
							"threshold", m.maxConsecutiveFailures)
					}
				}
				
				// 不健康节点：1 分钟后再探活（加快恢复检测）
				node.NextHealthCheckAt = now.Add(1 * time.Minute)
				failedCount++
				
				// 记录失败指标
				if m.metrics != nil {
					m.metrics.IncHealthCheck("failed")
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
		dialableCount := totalNodes - skippedUndialable
		unhealthyCount := 0
		for _, node := range allNodes {
			if node.Status == "unhealthy" {
				unhealthyCount++
			}
		}
		m.metrics.SetNodeCounts(totalNodes, dialableCount, unhealthyCount)
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
	Total    int
	OK       int
	Failed   int
	Skipped  int // 不可拨号（trojan/vless 等）节点数
	AvgMs    int
	MaxMs    int
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
				
				// 记录成功指标
				if m.metrics != nil {
					m.metrics.IncHealthCheck("success")
					m.metrics.ObserveNodeResponseTime(float64(res.Latency))
				}
			} else {
				node.LastHealthCheckStatus = "failed"
				node.ConsecutiveFailures++
				if node.ConsecutiveFailures >= 3 {
					node.Status = "unhealthy"
				}
				summary.Failed++
				
				// 记录失败指标
				if m.metrics != nil {
					m.metrics.IncHealthCheck("failed")
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

// getNodesFromCacheWithTTL 从缓存获取节点，检查 TTL（阶段 2 优化）
// 返回节点列表和缓存是否命中（未过期）
func (m *Manager) getNodesFromCacheWithTTL(subscriptionID int, now time.Time) ([]*Node, bool) {
	if value, ok := m.nodesCache.Load(subscriptionID); ok {
		entry := value.(*cacheEntry)
		// 检查是否过期
		if now.Before(entry.expiresAt) {
			return entry.nodes, true // 缓存命中
		}
		// 缓存过期，异步刷新
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			m.loadNodesIntoCache(ctx, subscriptionID)
		}()
		// 返回过期数据，避免阻塞
		return entry.nodes, false
	}
	return nil, false
}

// setCacheWithTTL 设置缓存，带 TTL（阶段 2 优化）
func (m *Manager) setCacheWithTTL(subscriptionID int, nodes []*Node, now time.Time) {
	entry := &cacheEntry{
		nodes:     nodes,
		expiresAt: now.Add(m.cacheTTL),
	}
	m.nodesCache.Store(subscriptionID, entry)
}

func (m *Manager) getAllActiveCachedNodes() []*Node {
	var allNodes []*Node
	m.nodesCache.Range(func(key, value interface{}) bool {
		entry := value.(*cacheEntry)
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

// extractDomain 从 URL 提取域名
func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
