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
type HTTPHealthChecker struct {
	timeout time.Duration
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

	// socks5 也走 http.ProxyURL：net/http 对 socks5:// 代理会用 SOCKS5 拨号，
	// 对 https 目标则通过该通道建立 CONNECT 式隧道。
	transport, err := newTransportForProxy(node.ProxyURL(), c.timeout, true)
	if err != nil {
		return 0, err
	}
	defer transport.CloseIdleConnections()

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
func elapsedMillis(start time.Time) int {
	ms := int(time.Since(start).Milliseconds())
	if ms < 1 {
		ms = 1
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
	for _, node := range nodes {
		if !node.Dialable() {
			// 不可拨号节点（trojan/vless）跳过，直接标记不可探活。
			out <- HealthCheckResult{
				NodeID:   node.ID,
				NodeName: node.Name,
				OK:       false,
				Err:      fmt.Errorf("node not dialable (protocol %q)", node.Protocol),
				CheckedAt: time.Now(),
			}
			continue
		}
		wg.Add(1)
		sem <- struct{}{} // 获取并发额度
		go func(n *Node) {
			defer wg.Done()
			defer func() { <-sem }() // 释放额度
			latency, err := c.Check(ctx, n)
			out <- HealthCheckResult{
				NodeID:   n.ID,
				NodeName: n.Name,
				OK:       err == nil,
				Latency:  latency,
				Err:      err,
				CheckedAt: time.Now(),
			}
		}(node)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
