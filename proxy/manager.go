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

// Manager 代理管理器
type Manager struct {
	store   Store
	parser  Parser
	checker HealthChecker
	// transportFactory 按订阅缓存可复用的 http.Transport（Stage 2：避免每次
	// SelectBestNode/探活都新建 Transport 导致连接泄漏）。
	transportFactory *TransportFactory
	// metrics 代理子系统 Prometheus 指标（Stage 3）。
	metrics *Metrics

	// 内存缓存：subscription_id -> nodes
	nodesCache sync.Map
	// 审计修复 (2026-08-29)：并发安全 P1-2 - per-subscription 锁保护并发写入
	cacheLocks sync.Map // subscription_id -> *sync.RWMutex

	// 配置
	autoRefreshInterval   time.Duration
	healthCheckInterval   time.Duration
	unknownDomainStrategy string // direct/proxy/probe

	// 审计修复 (2026-08-29)：问题 7 - 增加 context 用于优雅停止后台 goroutine。
	ctx    context.Context
	cancel context.CancelFunc
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewManager 创建代理管理器
func NewManager(store Store, parser Parser, checker HealthChecker) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store:                 store,
		parser:                parser,
		checker:               checker,
		transportFactory:      NewTransportFactory(nil),
		metrics:               NewMetrics(nil),
		autoRefreshInterval:   1 * time.Hour,
		healthCheckInterval:   5 * time.Minute,
		unknownDomainStrategy: "direct",
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

// SelectBestNode 选择最优节点
func (m *Manager) SelectBestNode(ctx context.Context, subscriptionID *int) (*Node, error) {
	var candidates []*Node
	
	if subscriptionID != nil {
		// 从指定订阅选择
		candidates = m.getNodesFromCache(*subscriptionID)
	} else {
		// 从所有 active 订阅选择
		candidates = m.getAllActiveCachedNodes()
	}
	
	if len(candidates) == 0 {
		// 尝试从数据库加载
		nodes, err := m.store.ListNodes(ctx, subscriptionID)
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}
		candidates = nodes
		// 审计修复 (2026-08-29)：问题 3 - 缓存未命中时回填缓存，避免后续请求继续打数据库。
		if subscriptionID != nil && len(nodes) > 0 {
			m.nodesCache.Store(*subscriptionID, nodes)
		}
	}
	
	// 过滤掉不健康、以及 Go 无法直接拨号的节点（trojan/vless 等需本地网桥）
	activeNodes := make([]*Node, 0, len(candidates))
	skippedUndialable := 0
	for _, node := range candidates {
		if node.Status != "active" || node.ConsecutiveFailures >= 3 {
			continue
		}
		if !node.Dialable() {
			skippedUndialable++
			continue
		}
		activeNodes = append(activeNodes, node)
	}

	if len(activeNodes) == 0 {
		if skippedUndialable > 0 {
			if m.metrics != nil {
				m.metrics.IncEgressSelection("undialable")
			}
			return nil, fmt.Errorf("no dialable proxy node: %d node(s) use protocols Go cannot proxy directly (trojan/vless/vmess/ss); expose them via a local mihomo/xray http or socks5 bridge and register that endpoint instead", skippedUndialable)
		}
		if m.metrics != nil {
			m.metrics.IncEgressSelection("none")
		}
		return nil, fmt.Errorf("no available active nodes")
	}

	if m.metrics != nil {
		m.metrics.IncEgressSelection("dialable")
	}
	
	// 按健康状态和响应时间排序
	sort.Slice(activeNodes, func(i, j int) bool {
		// 优先选择连续失败次数少的（最近探活健康的节点排前面）
		if activeNodes[i].ConsecutiveFailures != activeNodes[j].ConsecutiveFailures {
			return activeNodes[i].ConsecutiveFailures < activeNodes[j].ConsecutiveFailures
		}
		// 其次选择成功率高的
		if activeNodes[i].SuccessRate != activeNodes[j].SuccessRate {
			return activeNodes[i].SuccessRate > activeNodes[j].SuccessRate
		}
		// 最后选择响应时间快的
		return activeNodes[i].ResponseTimeMs < activeNodes[j].ResponseTimeMs
	})
	
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
		
		// 连续失败 3 次则标记为不健康
		if node.ConsecutiveFailures >= 3 {
			node.Status = "unhealthy"
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
	
	for subID, nodes := range nodesBySubscription {
		m.nodesCache.Store(subID, nodes)
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
	subs, err := m.store.ListSubscriptions(ctx)
	if err != nil {
		slog.Error("proxy: failed to list subscriptions for health check", "error", err)
		return
	}
	for _, sub := range subs {
		if sub.Status != "active" {
			continue
		}
		summary, herr := m.HealthCheckSubscription(ctx, sub.ID)
		if herr != nil {
			slog.Warn("proxy: subscription health check failed", "id", sub.ID, "name", sub.Name, "error", herr)
			continue
		}
		if summary.Total > 0 {
			slog.Info("proxy: subscription health check done",
				"id", sub.ID, "name", sub.Name,
				"total", summary.Total, "ok", summary.OK, "failed", summary.Failed)
		}
	}
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
		} else {
			node.LastHealthCheckStatus = "failed"
			node.ConsecutiveFailures++
			if node.ConsecutiveFailures >= 3 {
				node.Status = "unhealthy"
			}
			summary.Failed++
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
	
	// 存入缓存
	for subID, nodes := range nodesBySubscription {
		m.nodesCache.Store(subID, nodes)
	}
	
	return nil
}

func (m *Manager) loadNodesIntoCache(ctx context.Context, subscriptionID int) {
	nodes, err := m.store.ListNodes(ctx, &subscriptionID)
	if err != nil {
		slog.Error("proxy: failed to load nodes into cache", "error", err)
		return
	}
	m.nodesCache.Store(subscriptionID, nodes)
}

func (m *Manager) getNodesFromCache(subscriptionID int) []*Node {
	if value, ok := m.nodesCache.Load(subscriptionID); ok {
		return value.([]*Node)
	}
	return nil
}

func (m *Manager) getAllActiveCachedNodes() []*Node {
	var allNodes []*Node
	m.nodesCache.Range(func(key, value interface{}) bool {
		nodes := value.([]*Node)
		allNodes = append(allNodes, nodes...)
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
	m.nodesCache.Store(node.SubscriptionID, newNodes)
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
