package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// TransportFactory 按订阅缓存可复用的 http.Transport。
//
// 背景（Stage 2 优化目标）：之前每次 SelectBestNode / 探活都新建一个 http.Transport，
// 用完即弃、从不关闭空闲连接——既无法复用经由代理的出站连接（socket 耗尽风险），
// 又泄漏 idle 连接。本工厂按 subscription_id 缓存 Transport，命中同一订阅的多次请求
// 共享连接池；仅当选出的节点变化（代理出口 URL 不同）时才重建，并关闭旧 Transport 的
// 空闲连接。
//
// 注意：Transport 的 Proxy 字段由具体节点的代理 URL 决定。一个订阅下通常只有一个
// 被选中节点（网桥），因此按订阅缓存足够；若同一订阅有多个可选节点且来回切换，
// 会触发按节点粒度的重建，但频率很低。
type TransportFactory struct {
	mu sync.Mutex
	// subID -> 该订阅当前缓存的 Transport 及所选节点代理 URL（用于判断是否需要重建）。
	transports map[int]*subscriptionTransport

	// metrics 用于记录缓存大小和失效次数（可选）
	metrics *Metrics

	// 连接池与超时默认值（可通过构造参数微调）。
	maxIdleConns        int
	maxIdleConnsPerHost int
	idleConnTimeout     time.Duration
	tlsHandshakeTimeout time.Duration
	respHeaderTimeout   time.Duration
	expectContinue      time.Duration
	dialTimeout         time.Duration
}

type subscriptionTransport struct {
	transport *http.Transport
	proxyURL  string // 该 transport 对应的节点代理 URL（node.ProxyURL()）
}

// TransportFactoryConfig 可调的 Transport 参数。零值使用合理默认。
type TransportFactoryConfig struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	ExpectContinueTimeout time.Duration
	DialTimeout           time.Duration
}

// NewTransportFactory 创建 Transport 工厂。config 为 nil 或零值字段时使用默认。
func NewTransportFactory(config *TransportFactoryConfig) *TransportFactory {
	f := &TransportFactory{
		transports:          make(map[int]*subscriptionTransport),
		maxIdleConns:        100,
		maxIdleConnsPerHost: 10,
		idleConnTimeout:     90 * time.Second,
		tlsHandshakeTimeout: 10 * time.Second,
		respHeaderTimeout:   30 * time.Second,
		expectContinue:      time.Second,
		dialTimeout:         10 * time.Second,
	}
	if config != nil {
		if config.MaxIdleConns > 0 {
			f.maxIdleConns = config.MaxIdleConns
		}
		if config.MaxIdleConnsPerHost > 0 {
			f.maxIdleConnsPerHost = config.MaxIdleConnsPerHost
		}
		if config.IdleConnTimeout > 0 {
			f.idleConnTimeout = config.IdleConnTimeout
		}
		if config.TLSHandshakeTimeout > 0 {
			f.tlsHandshakeTimeout = config.TLSHandshakeTimeout
		}
		if config.ResponseHeaderTimeout > 0 {
			f.respHeaderTimeout = config.ResponseHeaderTimeout
		}
		if config.ExpectContinueTimeout > 0 {
			f.expectContinue = config.ExpectContinueTimeout
		}
		if config.DialTimeout > 0 {
			f.dialTimeout = config.DialTimeout
		}
	}
	return f
}

// newTransportForProxy 为给定代理 URL 构造一个带连接池的 Transport（供业务流量复用）。
// proxyURL 为空时返回直连（Proxy=nil）Transport。复用包级 newTransportForProxy 共享
// 实现，disableKeepAlives=false 以复用经由代理的出站连接。
func (f *TransportFactory) newTransportForProxy(proxyURL string) (*http.Transport, error) {
	tr, err := newTransportForProxy(proxyURL, f.tlsHandshakeTimeout, false)
	if err != nil {
		return nil, err
	}
	tr.MaxIdleConns = f.maxIdleConns
	tr.MaxIdleConnsPerHost = f.maxIdleConnsPerHost
	tr.IdleConnTimeout = f.idleConnTimeout
	tr.ResponseHeaderTimeout = f.respHeaderTimeout
	tr.ExpectContinueTimeout = f.expectContinue
	// 经由代理的出站连接可能较久，禁用强制 HTTP/2 以便对 http/https 代理更可控。
	tr.ForceAttemptHTTP2 = false
	return tr, nil
}

// Get 返回订阅对应的可复用 Transport，必要时按当前节点代理 URL 重建。
// proxyURL 为该订阅被选节点的 node.ProxyURL()（空串表示直连）。
func (f *TransportFactory) Get(subID int, proxyURL string) (*http.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if st, ok := f.transports[subID]; ok && st.proxyURL == proxyURL {
		// 缓存命中，更新缓存大小指标
		if f.metrics != nil {
			f.metrics.SetTransportCacheSize(len(f.transports))
		}
		return st.transport, nil
	}

	// 节点变化或首次：重建并关闭旧 transport 的空闲连接（避免泄漏）。
	if old, ok := f.transports[subID]; ok && old.transport != nil {
		old.transport.CloseIdleConnections()
		// 记录失效指标
		if f.metrics != nil {
			f.metrics.IncTransportInvalidation()
		}
	}

	tr, err := f.newTransportForProxy(proxyURL)
	if err != nil {
		return nil, err
	}
	f.transports[subID] = &subscriptionTransport{transport: tr, proxyURL: proxyURL}

	// 更新缓存大小指标
	if f.metrics != nil {
		f.metrics.SetTransportCacheSize(len(f.transports))
	}

	slog.Debug("proxy: transport rebuilt for subscription", "subscription_id", subID, "proxy_url", redactProxyURL(proxyURL))
	return tr, nil
}

// Invalidate 使订阅的缓存 Transport 失效（如节点被删除/订阅刷新后）。
// 下次 Get 会重建；这里先关闭空闲连接。
func (f *TransportFactory) Invalidate(subID int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.transports[subID]; ok && st.transport != nil {
		st.transport.CloseIdleConnections()
		delete(f.transports, subID)

		// 记录失效指标
		if f.metrics != nil {
			f.metrics.IncTransportInvalidation()
			f.metrics.SetTransportCacheSize(len(f.transports))
		}
	}
}

// CloseIdleConnections 关闭所有缓存 Transport 的空闲连接。应在 Manager.Stop 时调用。
func (f *TransportFactory) CloseIdleConnections() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, st := range f.transports {
		if st.transport != nil {
			st.transport.CloseIdleConnections()
		}
		delete(f.transports, id)
	}
	if f.metrics != nil {
		f.metrics.SetTransportCacheSize(0)
	}
}

// redactProxyURL 仅用于日志：去掉可能存在的 userinfo。
func redactProxyURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	return fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
}
