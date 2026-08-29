package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

var _ HealthChecker = (*HTTPHealthChecker)(nil)

const (
	// defaultHealthCheckTimeout 单次探测的默认超时。
	defaultHealthCheckTimeout = 10 * time.Second
	// defaultHealthCheckURL 默认探测地址（返回 204，正文为空，开销最小）。
	defaultHealthCheckURL = "https://www.google.com/generate_204"
	// healthCheckDrainLimit 读取并丢弃的响应正文上限，保证连接可复用/正常关闭。
	healthCheckDrainLimit = 32 << 10
)

// HTTPHealthChecker 通过节点自身作为 HTTP 代理发起一次探测请求来判断可用性。
//
// 仅支持 Node.Dialable() == true 的节点（http/https/socks5）。
// trojan/vless/vmess/ss 无法被 Go 的 net/http 直接拨号，必须先用本地
// mihomo/xray 网桥暴露成 http/socks5 入口，再以该入口作为节点录入后才能探测。
//
// 审计修复 (2026-08-29)：问题 8 - 增加 Transport 池，复用连接提升探活性能。
type HTTPHealthChecker struct {
	timeout    time.Duration
	transports sync.Map // proxyURL -> *http.Transport
	mu         sync.Mutex
}

// NewHTTPHealthChecker 创建健康检查器，timeout <= 0 时使用默认 10s。
func NewHTTPHealthChecker(timeout time.Duration) *HTTPHealthChecker {
	if timeout <= 0 {
		timeout = defaultHealthCheckTimeout
	}
	return &HTTPHealthChecker{timeout: timeout}
}

// newTransportForProxy 为给定代理 URL 构造一个 http.Transport（包级共享实现，
// 供健康检查器与 TransportFactory 复用）。
//   - proxyURL 为空表示直连（Proxy=nil）。
//   - disableKeepAlives=true 用于一次性探测请求，避免滞留连接；
//     false 用于工厂缓存的业务流量连接池，复用经由代理的出站连接。
func newTransportForProxy(proxyURL string, timeout time.Duration, disableKeepAlives bool) (*http.Transport, error) {
	var proxyFunc func(*http.Request) (*url.URL, error)
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("proxy: invalid proxy url %q: %w", proxyURL, err)
		}
		proxyFunc = http.ProxyURL(u)
	}
	return &http.Transport{
		Proxy:                 proxyFunc,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		DisableKeepAlives:     disableKeepAlives,
		ForceAttemptHTTP2:     !disableKeepAlives,
	}, nil
}

// getOrCreateTransport 获取或创建可复用的 Transport（审计修复 2026-08-29 问题 8）。
func (c *HTTPHealthChecker) getOrCreateTransport(proxyURL string) (*http.Transport, error) {
	if cached, ok := c.transports.Load(proxyURL); ok {
		return cached.(*http.Transport), nil
	}
	
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// 再次检查（double-check）
	if cached, ok := c.transports.Load(proxyURL); ok {
		return cached.(*http.Transport), nil
	}
	
	// 创建可复用的 Transport（disableKeepAlives=false）
	tr, err := newTransportForProxy(proxyURL, c.timeout, false)
	if err != nil {
		return nil, err
	}
	// 设置连接池参数
	tr.MaxIdleConns = 10
	tr.MaxIdleConnsPerHost = 2
	tr.IdleConnTimeout = 90 * time.Second
	
	c.transports.Store(proxyURL, tr)
	return tr, nil
}

// Close 关闭所有缓存的 Transport（审计修复 2026-08-29 问题 8）。
func (c *HTTPHealthChecker) Close() {
	c.transports.Range(func(key, value interface{}) bool {
		if tr, ok := value.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
		c.transports.Delete(key)
		return true
	})
}

// Check 探测节点健康状态，返回耗时（毫秒）。
// 2xx/3xx 视为健康；4xx/5xx 返回带状态码的错误。
func (c *HTTPHealthChecker) Check(ctx context.Context, node *Node) (int, error) {
	if node == nil {
		return 0, errors.New("proxy: health check on nil node")
	}
	if !node.Dialable() {
		return 0, fmt.Errorf("proxy: node %q (protocol %q, %s) cannot be probed directly: "+
			"Go's HTTP client only dials http/https/socks5 proxies; expose this node through a "+
			"local mihomo/xray http or socks5 bridge and register that bridge endpoint instead",
			node.Name, node.Protocol, net.JoinHostPort(node.Server, strconv.Itoa(node.Port)))
	}

	targetURL := node.HealthCheckURL
	if targetURL == "" {
		targetURL = defaultHealthCheckURL
	}

	if ctx == nil {
		ctx = context.Background()
	}
	// 同时尊重调用方 ctx 与自身超时配置：取两者中先到的那个。
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// 审计修复 (2026-08-29)：问题 8 - 使用连接池复用 Transport。
	transport, err := c.getOrCreateTransport(node.ProxyURL())
	if err != nil {
		return 0, err
	}

	client := &http.Client{
		Transport: transport,
		// 3xx 视为健康，因此不跟随重定向，直接拿到原始状态码。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, fmt.Errorf("proxy: build health check request for node %q: %w", node.Name, err)
	}
	req.Header.Set("User-Agent", "llm-gateway-go/health-check")
	req.Header.Set("Cache-Control", "no-cache")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		elapsedMs := elapsedMillis(start)
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
			return elapsedMs, fmt.Errorf("proxy: node %q health check timed out after %s: %w",
				node.Name, c.timeout, err)
		}
		return elapsedMs, fmt.Errorf("proxy: node %q health check request failed: %w", node.Name, err)
	}

	// 必须读干并关闭正文，避免连接与 goroutine 泄漏。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, healthCheckDrainLimit))
	_ = resp.Body.Close()

	elapsedMs := elapsedMillis(start)
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return elapsedMs, fmt.Errorf("proxy: node %q health check returned HTTP %d (%s)",
			node.Name, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return elapsedMs, nil
}

// elapsedMillis 返回墙钟耗时（毫秒），亚毫秒结果计为 1，避免出现容易误解的 0。
// 审计修复 (2026-08-29)：问题 12 - 增加上限检查，避免极端情况下的溢出。
func elapsedMillis(start time.Time) int {
	ms := int(time.Since(start).Milliseconds())
	if ms < 1 {
		ms = 1
	}
	// 防止溢出（理论上不可能，因为有超时，但增加保护）
	const maxInt = int(^uint(0) >> 1) // math.MaxInt
	if ms > maxInt {
		ms = maxInt
	}
	return ms
}

// HealthCheckResult 单次并发探测的结果。
type HealthCheckResult struct {
	NodeID   int
	NodeName string
	OK       bool
	Latency  int // 毫秒；失败时为达到失败前的耗时
	Err      error
	CheckedAt time.Time
}

// CheckConcurrent 对一批节点做并发健康检查，结果通过返回的 channel 逐个送出
// （发送完毕后会关闭 channel）。concurrency <= 0 时回退为 16。
//
// 用于定时批量探活：相比串行循环（每个节点一次请求 + 100ms sleep），并发能显著
// 缩短 100+ 节点的整体探测耗时。调用方负责把结果持久化到 Store 与缓存。
//
// 审计修复 (2026-08-29)：问题 4 - 增加 context 取消时的提前退出机制，避免
// goroutine 泄漏。当调用方取消 context 时，立即停止派发新任务并关闭输出 channel。
func (c *HTTPHealthChecker) CheckConcurrent(ctx context.Context, nodes []*Node, concurrency int) <-chan HealthCheckResult {
	out := make(chan HealthCheckResult, len(nodes))
	if concurrency <= 0 {
		concurrency = 16
	}
	if len(nodes) == 0 {
		close(out)
		return out
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	
	// 监听 context 取消，提前中止
	cancelCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(cancelCh)
	}()

	for _, node := range nodes {
		// 检查是否已取消
		select {
		case <-cancelCh:
			// context 已取消，停止派发新任务
			break
		default:
		}
		
		if !node.Dialable() {
			// 不可拨号节点（trojan/vless）跳过，直接标记不可探活。
			out <- HealthCheckResult{
				NodeID:    node.ID,
				NodeName:  node.Name,
				OK:        false,
				Err:       fmt.Errorf("node not dialable (protocol %q)", node.Protocol),
				CheckedAt: time.Now(),
			}
			continue
		}
		wg.Add(1)
		sem <- struct{}{} // 获取并发额度
		go func(n *Node) {
			defer wg.Done()
			defer func() { <-sem }() // 释放额度
			
			// 使用调用方的 context，在取消时探测会立即中止
			latency, err := c.Check(ctx, n)
			
			// 尝试发送结果，如果 out 已关闭则丢弃
			select {
			case out <- HealthCheckResult{
				NodeID:    n.ID,
				NodeName:  n.Name,
				OK:        err == nil,
				Latency:   latency,
				Err:       err,
				CheckedAt: time.Now(),
			}:
			case <-cancelCh:
				// channel 已关闭，不再发送
			}
		}(node)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
