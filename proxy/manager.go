package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// Manager 代理管理器
type Manager struct {
	store   Store
	parser  Parser
	checker HealthChecker
	
	// 内存缓存：subscription_id -> nodes
	nodesCache sync.Map
	
	// 配置
	autoRefreshInterval   time.Duration
	healthCheckInterval   time.Duration
	unknownDomainStrategy string // direct/proxy/probe
	
	stopCh chan struct{}
}

// NewManager 创建代理管理器
func NewManager(store Store, parser Parser, checker HealthChecker) *Manager {
	return &Manager{
		store:                 store,
		parser:                parser,
		checker:               checker,
		autoRefreshInterval:   1 * time.Hour,
		healthCheckInterval:   5 * time.Minute,
		unknownDomainStrategy: "direct",
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
	go m.refreshLoop()
	
	// 定时健康检查
	go m.healthCheckLoop()
}

// Stop 停止定时任务
func (m *Manager) Stop() {
	close(m.stopCh)
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
	}
	
	// 过滤掉不健康的节点
	activeNodes := make([]*Node, 0, len(candidates))
	for _, node := range candidates {
		if node.Status == "active" && node.ConsecutiveFailures < 3 {
			activeNodes = append(activeNodes, node)
		}
	}
	
	if len(activeNodes) == 0 {
		return nil, fmt.Errorf("no available active nodes")
	}
	
	// 按健康状态和响应时间排序
	sort.Slice(activeNodes, func(i, j int) bool {
		// 优先选择成功率高的
		if activeNodes[i].SuccessRate != activeNodes[j].SuccessRate {
			return activeNodes[i].SuccessRate > activeNodes[j].SuccessRate
		}
		// 其次选择响应时间快的
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
	
	// 3. 更新数据库
	// 删除旧节点
	if err := m.store.DeleteNodesBySubscription(ctx, subscriptionID); err != nil {
		return fmt.Errorf("delete old nodes: %w", err)
	}
	
	// 插入新节点
	for _, node := range nodes {
		node.SubscriptionID = subscriptionID
		if err := m.store.CreateNode(ctx, node); err != nil {
			slog.Warn("proxy: failed to create node", "error", err, "name", node.Name)
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

// GetProxyTransport 获取代理 Transport
func (m *Manager) GetProxyTransport(ctx context.Context, subscriptionID *int) (*http.Transport, error) {
	node, err := m.SelectBestNode(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	
	proxyURLStr := node.ProxyURL()
	if proxyURLStr == "" {
		return nil, fmt.Errorf("unsupported proxy protocol: %s", node.Protocol)
	}
	
	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}
	
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		// TLS 配置
		TLSHandshakeTimeout: 10 * time.Second,
		// 连接池配置
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		// 超时配置
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	
	return transport, nil
}

// 内部方法

func (m *Manager) refreshLoop() {
	ticker := time.NewTicker(m.autoRefreshInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			m.refreshAllSubscriptions()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) healthCheckLoop() {
	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			m.healthCheckAllNodes()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) refreshAllSubscriptions() {
	ctx := context.Background()
	subs, err := m.store.ListSubscriptions(ctx)
	if err != nil {
		slog.Error("proxy: failed to list subscriptions", "error", err)
		return
	}
	
	for _, sub := range subs {
		if sub.Status == "active" {
			if err := m.RefreshSubscription(ctx, sub.ID); err != nil {
				slog.Error("proxy: failed to refresh subscription",
					"id", sub.ID,
					"name", sub.Name,
					"error", err)
			}
		}
	}
}

func (m *Manager) healthCheckAllNodes() {
	ctx := context.Background()
	nodes, err := m.store.ListNodes(ctx, nil)
	if err != nil {
		slog.Error("proxy: failed to list nodes", "error", err)
		return
	}
	
	for _, node := range nodes {
		if node.Status == "active" || node.Status == "unhealthy" {
			if err := m.HealthCheckNode(ctx, node.ID); err != nil {
				slog.Warn("proxy: health check error",
					"node_id", node.ID,
					"node_name", node.Name,
					"error", err)
			}
			
			// 避免过快检查
			time.Sleep(100 * time.Millisecond)
		}
	}
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

func (m *Manager) updateNodeInCache(node *Node) {
	nodes := m.getNodesFromCache(node.SubscriptionID)
	if nodes == nil {
		return
	}
	
	// 更新缓存中的节点
	for i, n := range nodes {
		if n.ID == node.ID {
			nodes[i] = node
			break
		}
	}
	m.nodesCache.Store(node.SubscriptionID, nodes)
}

// extractDomain 从 URL 提取域名
func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
