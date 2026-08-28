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

	// ProxyURL 可能带凭据，绝不能出现在错误信息里。
	proxyURL, err := url.Parse(node.ProxyURL())
	if err != nil {
		return 0, fmt.Errorf("proxy: node %q has an invalid proxy endpoint %s://%s",
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
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		TLSHandshakeTimeout:   c.timeout,
		ResponseHeaderTimeout: c.timeout,
		ExpectContinueTimeout: time.Second,
		DisableKeepAlives:     true, // 探测是一次性请求，不留连接
		ForceAttemptHTTP2:     true,
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
