package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TestProxyURLEncodesPortAsDigits 锁定一个曾经的真实 bug：端口用 string(rune(port))
// 拼接，8443 会变成一个 CJK 字符，生成的代理 URL 完全不可用。
func TestNewMetricsReusesExistingCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	first := NewMetrics(reg)
	second := NewMetrics(reg)
	if first.nodesTotal != second.nodesTotal || first.healthFailuresTotal != second.healthFailuresTotal {
		t.Fatal("NewMetrics must reuse collectors already registered on the same registry")
	}
}

func TestProxyURLEncodesPortAsDigits(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want string
	}{
		{
			name: "http without auth",
			node: Node{Protocol: "http", Server: "127.0.0.1", Port: 7897},
			want: "http://127.0.0.1:7897",
		},
		{
			name: "socks5 with auth escapes special chars",
			node: Node{Protocol: "socks5", Server: "proxy.example", Port: 1080, Username: "u", Password: "p@ss:w/rd"},
			want: "socks5://u:p%40ss%3Aw%2Frd@proxy.example:1080",
		},
		{
			name: "https username only",
			node: Node{Protocol: "https", Server: "eg.example", Port: 443, Username: "only"},
			want: "https://only@eg.example:443",
		},
		{
			name: "ipv6 host is bracketed",
			node: Node{Protocol: "http", Server: "::1", Port: 3128},
			want: "http://[::1]:3128",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.node.ProxyURL()
			if got != tc.want {
				t.Fatalf("ProxyURL() = %q, want %q", got, tc.want)
			}
			// 生成的 URL 必须能被 http.ProxyURL 的解析路径接受
			if _, err := http.NewRequest(http.MethodGet, got, nil); err != nil {
				t.Fatalf("generated proxy URL is unparseable: %v", err)
			}
		})
	}
}

// TestUndialableProtocols 保证 trojan/vless 这类协议不会被当成可用代理。
// Go 的 net/http 无法直接拨号它们，必须先经本地 mihomo/xray 网桥。
func TestUndialableProtocols(t *testing.T) {
	for _, proto := range []string{"trojan", "vless", "vmess", "ss", "hysteria2", ""} {
		n := &Node{Protocol: proto, Server: "example.com", Port: 443}
		if n.Dialable() {
			t.Errorf("protocol %q must not be dialable", proto)
		}
		if got := n.ProxyURL(); got != "" {
			t.Errorf("protocol %q must yield empty ProxyURL, got %q", proto, got)
		}
	}
}

func TestDialableRejectsInvalidHostPort(t *testing.T) {
	for _, n := range []Node{
		{Protocol: "http", Server: "", Port: 8080},
		{Protocol: "http", Server: "h", Port: 0},
		{Protocol: "http", Server: "h", Port: -1},
		{Protocol: "http", Server: "h", Port: 70000},
	} {
		if n.Dialable() {
			t.Errorf("node %+v must not be dialable", n)
		}
	}
}

const clashFixture = `
proxies:
- name: bridge-http
  type: http
  server: 127.0.0.1
  port: 7897
- name: bridge-http-tls
  type: http
  server: sec.example
  port: 8443
  tls: true
- name: "\U0001F1ED\U0001F1F0|香港-中转 01"
  type: trojan
  server: R1.tube-cat.com
  port: 9115
  password: super-secret-pass
  sni: hk.catxstar.com
  skip-cert-verify: true
- name: NPS-VPN
  type: vless
  server: 115.29.212.252
  port: 8443
  uuid: 0ca7481a-f5f7-418a-829f-ac8048e7022c
  flow: xtls-rprx-vision
  reality-opts:
    public-key: PJ6cxxKs
    short-id: 87e50edb
- name: broken-no-server
  type: trojan
  server: ""
  port: 443
`

// TestParseClashYAML 覆盖真实订阅格式（Clash/Mihomo YAML）。
func TestParseClashYAML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // test fixture write
		w.Write([]byte(clashFixture))
	}))
	defer srv.Close()

	nodes, err := NewMultiFormatParser().Parse(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// broken-no-server 应被跳过而不是让整个解析失败
	if len(nodes) != 4 {
		t.Fatalf("got %d nodes, want 4 (invalid entry must be skipped)", len(nodes))
	}

	byName := map[string]*Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}

	// http 网桥必须可拨号
	bridge, ok := byName["bridge-http"]
	if !ok {
		t.Fatal("bridge-http missing")
	}
	if !bridge.Dialable() || bridge.ProxyURL() != "http://127.0.0.1:7897" {
		t.Fatalf("bridge dialable=%v url=%q", bridge.Dialable(), bridge.ProxyURL())
	}

	// http + tls:true 应映射为 https
	if tlsNode := byName["bridge-http-tls"]; tlsNode == nil || tlsNode.Protocol != ProtocolHTTPS {
		t.Fatalf("http+tls should map to https, got %+v", tlsNode)
	}

	// emoji/CJK 名称必须原样保留，且 trojan 不可拨号
	hk, ok := byName["🇭🇰|香港-中转 01"]
	if !ok {
		t.Fatalf("emoji/CJK name not preserved; names=%v", keysOf(byName))
	}
	if hk.Protocol != "trojan" || hk.Dialable() {
		t.Fatalf("trojan node wrong: proto=%s dialable=%v", hk.Protocol, hk.Dialable())
	}
	if hk.Password != "super-secret-pass" {
		t.Fatalf("trojan password not captured")
	}

	// vless 的协议特定字段必须落到 Config，不能丢失
	vl, ok := byName["NPS-VPN"]
	if !ok {
		t.Fatal("NPS-VPN missing")
	}
	if vl.Config["uuid"] != "0ca7481a-f5f7-418a-829f-ac8048e7022c" {
		t.Fatalf("vless uuid lost, config=%v", vl.Config)
	}
	if _, ok := vl.Config["reality-opts"]; !ok {
		t.Fatalf("nested reality-opts lost, config=%v", vl.Config)
	}
}

// TestSelectBestNodeUndialableGivesActionableError 保证「导入了一堆节点但一个都用不了」
// 时给出的是可操作的提示，而不是含糊的 no nodes。
func TestSelectBestNodeUndialableGivesActionableError(t *testing.T) {
	store := &fakeStore{nodes: []*Node{
		{ID: 1, Protocol: "trojan", Server: "a", Port: 443, Status: "active"},
		{ID: 2, Protocol: "vless", Server: "b", Port: 8443, Status: "active"},
	}}
	mgr := NewManager(store, nil, nil)

	_, err := mgr.SelectBestNode(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when no node is dialable")
	}
	for _, want := range []string{"dialable", "mihomo", "socks5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q for operators; got: %v", want, err)
		}
	}
}

func TestSelectBestNodeRecordsExactlyOneOutcome(t *testing.T) {
	reg := prometheus.NewRegistry()
	mgr := NewManager(&fakeStore{nodes: []*Node{
		{ID: 1, Protocol: "trojan", Server: "a", Port: 443, Status: "active"},
	}}, nil, nil)
	mgr.metrics = NewMetrics(reg)
	mgr.transportFactory.metrics = mgr.metrics

	if _, err := mgr.SelectBestNode(context.Background(), nil); err == nil {
		t.Fatal("SelectBestNode should fail without a dialable node")
	}
	if got := getCounterValue(t, mgr.metrics.nodeSelectionTotal, "no_dialable"); got != 1 {
		t.Fatalf("no_dialable selections = %v, want 1", got)
	}
	if got := getCounterValue(t, mgr.metrics.nodeSelectionTotal, "success"); got != 0 {
		t.Fatalf("success selections = %v, want 0", got)
	}
}

func TestSelectBestNodeRecordsStoreError(t *testing.T) {
	reg := prometheus.NewRegistry()
	mgr := NewManager(&fakeStore{listErr: errors.New("database unavailable")}, nil, nil)
	mgr.metrics = NewMetrics(reg)
	mgr.transportFactory.metrics = mgr.metrics

	if _, err := mgr.SelectBestNode(context.Background(), nil); err == nil {
		t.Fatal("SelectBestNode should return the store error")
	}
	if got := getCounterValue(t, mgr.metrics.nodeSelectionTotal, "store_error"); got != 1 {
		t.Fatalf("store_error selections = %v, want 1", got)
	}
	if got := getCounterValue(t, mgr.metrics.nodeSelectionTotal, "success"); got != 0 {
		t.Fatalf("success selections = %v, want 0", got)
	}
}

func TestManagerSelectionAPIs(t *testing.T) {
	store := &fakeStore{nodes: []*Node{
		{ID: 1, SubscriptionID: 7, Protocol: ProtocolHTTP, Server: "fast", Port: 8080, Status: "active", SuccessRate: 1, ResponseTimeMs: 10, Location: "US"},
		{ID: 2, SubscriptionID: 7, Protocol: ProtocolHTTP, Server: "slow", Port: 8081, Status: "active", SuccessRate: 1, ResponseTimeMs: 20, Location: "US"},
	}}
	mgr := NewManager(store, nil, nil)
	mgr.SetLoadBalanceStrategy(StrategyRoundRobin)

	for i, want := range []int{1, 2, 1} {
		node, err := mgr.SelectNodeWithStrategy(context.Background(), intPtr(7), "")
		if err != nil {
			t.Fatalf("round robin selection %d: %v", i, err)
		}
		if node.ID != want {
			t.Fatalf("round robin selection %d = node %d, want %d", i, node.ID, want)
		}
	}

	best, err := mgr.SelectBestNode(context.Background(), intPtr(7))
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	if best.ID != 1 {
		t.Fatalf("SelectBestNode must remain best-only, got node %d", best.ID)
	}
}

func TestManagerLocationAndCandidateFilters(t *testing.T) {
	store := &fakeStore{nodes: []*Node{
		{ID: 1, SubscriptionID: 9, Protocol: ProtocolHTTP, Server: "decrypt-failed", Port: 8080, Status: "active", PasswordDecryptFailed: true, SuccessRate: 1, ResponseTimeMs: 1, Location: "CN"},
		{ID: 2, SubscriptionID: 9, Protocol: ProtocolHTTP, Server: "too-many-failures", Port: 8081, Status: "active", ConsecutiveFailures: 2, SuccessRate: 1, ResponseTimeMs: 2, Location: "CN"},
		{ID: 3, SubscriptionID: 9, Protocol: ProtocolHTTP, Server: "us", Port: 8082, Status: "active", SuccessRate: 1, ResponseTimeMs: 3, Location: "US"},
	}}
	mgr := NewManager(store, nil, nil)
	mgr.SetAutoDisablePolicy(2, true, false)
	mgr.SetLocationAffinity(AffinityRequireSame)

	if _, err := mgr.SelectNodeWithLocation(context.Background(), intPtr(9), "", "CN"); err == nil {
		t.Fatal("expected no node when required location has only filtered candidates")
	}

	node, err := mgr.SelectNodeWithStrategy(context.Background(), intPtr(9), "")
	if err != nil {
		t.Fatalf("SelectNodeWithStrategy: %v", err)
	}
	if node.ID != 3 {
		t.Fatalf("selected node %d, want the only eligible node 3", node.ID)
	}
}

func TestManagerStartStopAreIdempotent(t *testing.T) {
	checker := &closableHealthChecker{}
	mgr := NewManager(&fakeStore{}, nil, checker)
	mgr.Start()
	mgr.Start()
	mgr.Stop()
	mgr.Stop()

	if checker.closeCalls != 1 {
		t.Fatalf("checker Close calls = %d, want 1", checker.closeCalls)
	}
}

func intPtr(v int) *int { return &v }

func TestManagerHealthCheckPolicyKeepsHealthyWhenAutoDisableDisabled(t *testing.T) {
	mgr := NewManager(&fakeStore{}, nil, nil)
	mgr.SetAutoDisablePolicy(2, false, true)
	node := &Node{Status: "active", ConsecutiveFailures: 1}

	mgr.applyHealthCheckResult(node, false, 17, time.Unix(123, 0))
	mgr.applyHealthCheckResult(node, false, 18, time.Unix(124, 0))

	if node.ConsecutiveFailures != 3 {
		t.Fatalf("failures = %d, want 3", node.ConsecutiveFailures)
	}
	if node.Status != "active" {
		t.Fatalf("status = %q, want active when auto-disable is disabled", node.Status)
	}
	if node.LastHealthCheckStatus != "failed" {
		t.Fatalf("health status = %q, want failed", node.LastHealthCheckStatus)
	}
}

func TestManagerHealthCheckPolicyKeepsUnhealthyWhenAutoRecoverDisabled(t *testing.T) {
	mgr := NewManager(&fakeStore{}, nil, nil)
	mgr.SetAutoDisablePolicy(3, true, false)
	node := &Node{Status: "unhealthy", ConsecutiveFailures: 4, SuccessRate: 0.5}

	mgr.applyHealthCheckResult(node, true, 42, time.Unix(123, 0))

	if node.Status != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy when auto-recover is disabled", node.Status)
	}
	if node.ConsecutiveFailures != 0 {
		t.Fatalf("failures = %d, want 0", node.ConsecutiveFailures)
	}
	if node.ResponseTimeMs != 42 {
		t.Fatalf("latency = %d, want 42", node.ResponseTimeMs)
	}
	if node.SuccessRate <= 0.5 {
		t.Fatalf("success rate = %v, want updated value above 0.5", node.SuccessRate)
	}
}

func TestManagerHealthCheckPolicyUsesConfiguredThreshold(t *testing.T) {
	mgr := NewManager(&fakeStore{}, nil, nil)
	mgr.SetAutoDisablePolicy(2, true, true)
	node := &Node{Status: "active"}

	mgr.applyHealthCheckResult(node, false, 0, time.Unix(123, 0))
	if node.Status != "active" {
		t.Fatalf("status after first failure = %q, want active", node.Status)
	}
	mgr.applyHealthCheckResult(node, false, 0, time.Unix(124, 0))
	if node.Status != "unhealthy" {
		t.Fatalf("status after threshold failure = %q, want unhealthy", node.Status)
	}
}

type closableHealthChecker struct {
	closeCalls int
}

func (c *closableHealthChecker) Check(context.Context, *Node) (int, error) { return 0, nil }

func (c *closableHealthChecker) CheckConcurrent(context.Context, []*Node, int) <-chan HealthCheckResult {
	out := make(chan HealthCheckResult)
	close(out)
	return out
}

func (c *closableHealthChecker) Close() { c.closeCalls++ }

func TestSelectBestNodePicksDialableAndFastest(t *testing.T) {
	store := &fakeStore{nodes: []*Node{
		{ID: 1, Protocol: "trojan", Server: "a", Port: 443, Status: "active", SuccessRate: 1, ResponseTimeMs: 1},
		{ID: 2, Protocol: "http", Server: "slow", Port: 8080, Status: "active", SuccessRate: 1, ResponseTimeMs: 900},
		{ID: 3, Protocol: "socks5", Server: "fast", Port: 1080, Status: "active", SuccessRate: 1, ResponseTimeMs: 30},
		{ID: 4, Protocol: "http", Server: "dead", Port: 8081, Status: "active", SuccessRate: 1, ResponseTimeMs: 5, ConsecutiveFailures: 3},
	}}
	mgr := NewManager(store, nil, nil)

	best, err := mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	if best.ID != 3 {
		t.Fatalf("expected fastest dialable node id=3, got id=%d (%s)", best.ID, best.Server)
	}
}

func TestHealthCheckerRejectsUndialableNode(t *testing.T) {
	c := NewHTTPHealthChecker(2 * time.Second)
	_, err := c.Check(context.Background(), &Node{Protocol: "trojan", Server: "a", Port: 443})
	if err == nil {
		t.Fatal("expected error for undialable node")
	}
}

// TestCheckConcurrentStopsOnContextCancel 二次审计修复 (2026-08-29)：
// 之前 select 里的 break 实际只跳出 select，for 循环仍会派发后续节点。
// 取消 ctx 后必须立刻停止派发，结果 channel 也必须正常关闭（调用方 range 不阻塞）。
func TestCheckConcurrentStopsOnContextCancel(t *testing.T) {
	c := NewHTTPHealthChecker(2 * time.Second)
	// 全部是 dialable 节点 + 默认 HealthCheckURL = google generate_204，
	// 在测试机网络受限情况下大多会失败但耗时稳定，便于观察取消行为。
	nodes := make([]*Node, 0, 50)
	for i := 0; i < 50; i++ {
		nodes = append(nodes, &Node{
			ID:       i + 1,
			Name:     "n",
			Protocol: ProtocolHTTP,
			Server:   "127.0.0.1",
			Port:     1,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	out := c.CheckConcurrent(ctx, nodes, 4)

	// 立即取消；派发循环必须及时退出，不能继续启动剩余 goroutine。
	cancel()

	done := make(chan struct{})
	var count int
	go func() {
		defer close(done)
		for range out {
			count++
		}
	}()

	select {
	case <-done:
		// 收到至少 0 个结果是允许的，重点是 channel 关闭、range 退出。
	case <-time.After(5 * time.Second):
		t.Fatal("CheckConcurrent output channel did not close after context cancel")
	}
	_ = count
}

// TestRedactErrCoversKVForm 二次审计修复 (2026-08-29)：
// redactErr 现在调用 SanitizeSecrets，能覆盖 password=xxx / token=xxx 等键值对形式，
// 不止 URL userinfo。
func TestRedactErrCoversKVForm(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		mustHave []string
		mustMiss []string
	}{
		{
			name:     "url userinfo",
			in:       "dial tcp socks5://user:secret@host:1080: connection refused",
			mustHave: []string{"[REDACTED]@"},
			mustMiss: []string{"user:secret"},
		},
		{
			name:     "kv form password",
			in:       `connect failed password=TopSecret123 host=1.2.3.4`,
			mustHave: []string{"[REDACTED]"},
			mustMiss: []string{"TopSecret123"},
		},
		{
			name:     "kv form token",
			in:       "auth: token=abc.def.ghi ; uuid=11111111-2222-3333-4444-555555555555",
			mustHave: []string{"[REDACTED]"},
			mustMiss: []string{"abc.def.ghi", "11111111-2222-3333-4444-555555555555"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactErr(fakeErr(tc.in))
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("redactErr(%q) = %q; missing %q", tc.in, got, want)
				}
			}
			for _, banned := range tc.mustMiss {
				if strings.Contains(got, banned) {
					t.Errorf("redactErr(%q) = %q; leaked %q", tc.in, got, banned)
				}
			}
		})
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

func keysOf(m map[string]*Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// fakeStore 只实现 SelectBestNode 需要的 ListNodes，其余方法返回零值。
type fakeStore struct {
	Store
	nodes        []*Node
	listErr      error
	subscription *Subscription
	listCalls    int
}

func (f *fakeStore) GetSubscription(_ context.Context, id int) (*Subscription, error) {
	if f.subscription == nil || f.subscription.ID != id {
		return nil, errors.New("subscription not found")
	}
	return f.subscription, nil
}

func (f *fakeStore) UpdateSubscription(_ context.Context, sub *Subscription) error {
	f.subscription = sub
	return nil
}

func (f *fakeStore) DeleteNodesBySubscription(_ context.Context, subscriptionID int) error {
	kept := f.nodes[:0]
	for _, node := range f.nodes {
		if node.SubscriptionID != subscriptionID {
			kept = append(kept, node)
		}
	}
	f.nodes = kept
	return nil
}

func (f *fakeStore) CreateNode(_ context.Context, node *Node) error {
	f.nodes = append(f.nodes, node)
	return nil
}

func (f *fakeStore) ListNodes(_ context.Context, subscriptionID *int) ([]*Node, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	if subscriptionID == nil {
		return f.nodes, nil
	}
	var out []*Node
	for _, n := range f.nodes {
		if n.SubscriptionID == *subscriptionID {
			out = append(out, n)
		}
	}
	return out, nil
}

func TestManagerGetProxyTransportForNodeUsesSpecifiedNode(t *testing.T) {
	mgr := NewManager(&fakeStore{}, nil, nil)
	subscriptionID := 7
	selected := &Node{
		ID:             42,
		SubscriptionID: subscriptionID,
		Protocol:       ProtocolHTTP,
		Server:         "selected-proxy.example",
		Port:           8080,
		Status:         "active",
	}

	transport, err := mgr.GetProxyTransportForNode(&subscriptionID, selected)
	if err != nil {
		t.Fatalf("GetProxyTransportForNode: %v", err)
	}
	proxyURL, err := transport.Proxy(&http.Request{URL: mustParseURL(t, "https://upstream.example")})
	if err != nil {
		t.Fatalf("transport Proxy: %v", err)
	}
	if got, want := proxyURL.String(), selected.ProxyURL(); got != want {
		t.Fatalf("transport proxy URL = %q, want selected node URL %q", got, want)
	}
}

func TestRefreshSubscriptionInvalidatesCachedTransport(t *testing.T) {
	const subscriptionID = 7
	oldNode := &Node{
		ID:             1,
		SubscriptionID: subscriptionID,
		Protocol:       ProtocolHTTP,
		Server:         "old-proxy.example",
		Port:           8080,
		Status:         "active",
	}
	store := &fakeStore{
		nodes: []*Node{oldNode},
		subscription: &Subscription{
			ID:           subscriptionID,
			Name:         "test",
			SubscribeURL: "https://subscription.example",
			Status:       "active",
		},
	}
	mgr := NewManager(store, staticParser{nodes: []*Node{{
		Name:     "new",
		Protocol: ProtocolHTTP,
		Server:   "new-proxy.example",
		Port:     8081,
		Status:   "active",
	}}}, nil)

	before, err := mgr.GetProxyTransportForNode(intPtr(subscriptionID), oldNode)
	if err != nil {
		t.Fatalf("cache initial transport: %v", err)
	}
	if err := mgr.RefreshSubscription(context.Background(), subscriptionID); err != nil {
		t.Fatalf("RefreshSubscription: %v", err)
	}
	after, err := mgr.GetProxyTransportForNode(intPtr(subscriptionID), oldNode)
	if err != nil {
		t.Fatalf("get transport after refresh: %v", err)
	}
	if after == before {
		t.Fatal("RefreshSubscription must invalidate the subscription transport cache")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

type staticParser struct {
	nodes []*Node
	err   error
}

func (p staticParser) Parse(context.Context, string) ([]*Node, error) {
	return p.nodes, p.err
}

func TestRefreshSubscriptionPersistsSanitizedLastError(t *testing.T) {
	const subscriptionID = 8
	subscribeURL := "https://user:password@subscription.example/path-token/feed?token=query-secret"
	store := &fakeStore{subscription: &Subscription{
		ID:           subscriptionID,
		Name:         "test",
		SubscribeURL: subscribeURL,
		Status:       "active",
	}}
	parserErr := fmt.Errorf("GET %s failed: password=body-secret", subscribeURL)
	mgr := NewManager(store, staticParser{err: parserErr}, nil)

	if err := mgr.RefreshSubscription(context.Background(), subscriptionID); err == nil {
		t.Fatal("RefreshSubscription unexpectedly succeeded")
	}
	for _, secret := range []string{"user:password", "path-token", "query-secret", "body-secret"} {
		if strings.Contains(store.subscription.LastError, secret) {
			t.Fatalf("persisted LastError leaked %q: %s", secret, store.subscription.LastError)
		}
	}
	if !strings.Contains(store.subscription.LastError, "https://subscription.example/redacted") ||
		!strings.Contains(store.subscription.LastError, "[REDACTED]") {
		t.Fatalf("persisted LastError = %q, want sanitized URL and credentials", store.subscription.LastError)
	}
}

func TestManagerTargetedSelectionKeepsSubscriptionCachesIndependent(t *testing.T) {
	store := &fakeStore{}
	mgr := NewManager(store, nil, nil)
	mgr.setCacheWithTTL(1, []*Node{{
		ID: 101, SubscriptionID: 1, Name: "subscription-one", Protocol: ProtocolHTTP,
		Server: "one.local", Port: 8080, Status: "active", ResponseTimeMs: 10,
	}}, time.Now())
	mgr.setCacheWithTTL(2, []*Node{{
		ID: 202, SubscriptionID: 2, Name: "subscription-two", Protocol: ProtocolHTTP,
		Server: "two.local", Port: 8081, Status: "active", ResponseTimeMs: 10,
	}}, time.Now())

	one, err := mgr.SelectBestNode(context.Background(), intPtr(1))
	if err != nil || one.ID != 101 {
		t.Fatalf("subscription one selection = node=%v err=%v", one, err)
	}
	two, err := mgr.SelectBestNode(context.Background(), intPtr(2))
	if err != nil || two.ID != 202 {
		t.Fatalf("subscription two selection = node=%v err=%v", two, err)
	}
	if store.listCalls != 0 {
		t.Fatalf("targeted cache selections caused %d store lookups, want 0", store.listCalls)
	}
	cachedOne, _, present := mgr.getNodesFromCacheWithTTL(1, time.Now())
	if !present || len(cachedOne) != 1 || cachedOne[0].ID != 101 {
		t.Fatalf("subscription one cache was replaced: present=%v nodes=%+v", present, cachedOne)
	}
}

func TestManagerCacheUsesEmptySnapshotAsNegativeCache(t *testing.T) {
	store := &fakeStore{}
	mgr := NewManager(store, nil, nil)
	const subscriptionID = 11
	mgr.setCacheWithTTL(subscriptionID, []*Node{}, time.Now())

	if _, err := mgr.SelectBestNode(context.Background(), intPtr(subscriptionID)); err == nil {
		t.Fatal("empty cached subscription should have no selectable node")
	}
	if store.listCalls != 0 {
		t.Fatalf("empty cached subscription caused %d store lookups, want 0", store.listCalls)
	}
}

func TestManagerCacheReturnsDeepIsolatedNodeSnapshot(t *testing.T) {
	mgr := NewManager(&fakeStore{}, nil, nil)
	original := &Node{
		ID:       1,
		Protocol: ProtocolHTTP,
		Server:   "bridge.local",
		Port:     7897,
		Status:   "active",
		Config: map[string]interface{}{
			"nested": map[string]interface{}{"token": "secret"},
			"items":  []interface{}{map[string]interface{}{"value": "one"}},
		},
	}
	mgr.setCacheWithTTL(3, []*Node{original}, time.Now())

	snapshot, _, present := mgr.getNodesFromCacheWithTTL(3, time.Now())
	if !present || len(snapshot) != 1 {
		t.Fatalf("cache snapshot present=%v len=%d", present, len(snapshot))
	}
	snapshot[0].Server = "mutated.local"
	snapshot[0].Config["nested"].(map[string]interface{})["token"] = "mutated"
	snapshot[0].Config["items"].([]interface{})[0].(map[string]interface{})["value"] = "mutated"

	again, _, _ := mgr.getNodesFromCacheWithTTL(3, time.Now())
	if again[0].Server != "bridge.local" {
		t.Fatalf("cached server mutated through snapshot: %q", again[0].Server)
	}
	if got := again[0].Config["nested"].(map[string]interface{})["token"]; got != "secret" {
		t.Fatalf("nested config mutated through snapshot: %v", got)
	}
	if got := again[0].Config["items"].([]interface{})[0].(map[string]interface{})["value"]; got != "one" {
		t.Fatalf("slice config mutated through snapshot: %v", got)
	}
}

func TestHTTPHealthCheckerCheckConcurrentAcceptsNilContext(t *testing.T) {
	checker := NewHTTPHealthChecker(100 * time.Millisecond)
	out := checker.CheckConcurrent(nil, []*Node{{ID: 7, Protocol: "trojan", Server: "node", Port: 443}}, 1)
	results := make([]HealthCheckResult, 0, 1)
	for result := range out {
		results = append(results, result)
	}
	if len(results) != 1 || results[0].OK || results[0].Err == nil {
		t.Fatalf("nil-context result = %+v, want one failed result", results)
	}
}

func TestTransportFactoryCachesPerSubscription(t *testing.T) {
	f := NewTransportFactory(nil)

	t1, err := f.Get(1, "http://127.0.0.1:7897")
	if err != nil {
		t.Fatalf("Get #1: %v", err)
	}
	// 同订阅 + 同代理 URL：必须复用，不应新建。
	t2, err := f.Get(1, "http://127.0.0.1:7897")
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if t1 != t2 {
		t.Fatal("expected cached transport to be reused for same subscription+proxyURL")
	}

	// 不同订阅：必须是另一个 Transport 实例。
	t3, err := f.Get(2, "http://127.0.0.1:7897")
	if err != nil {
		t.Fatalf("Get #3: %v", err)
	}
	if t3 == t1 {
		t.Fatal("different subscription must not share transport instance")
	}

	// 同一订阅但代理 URL 变化（节点切换）：应重建为新的实例。
	t4, err := f.Get(1, "socks5://127.0.0.1:7898")
	if err != nil {
		t.Fatalf("Get #4: %v", err)
	}
	if t4 == t1 {
		t.Fatal("changed proxy url must rebuild transport")
	}
	// 切回原 URL：再次复用（缓存已更新为新 URL 对应的实例）。
	t5, err := f.Get(1, "socks5://127.0.0.1:7898")
	if err != nil {
		t.Fatalf("Get #5: %v", err)
	}
	if t5 != t4 {
		t.Fatal("expected cached transport to be reused after url change")
	}

	// 直连（空代理 URL）也应可构造且不 panic。
	if _, err := f.Get(3, ""); err != nil {
		t.Fatalf("Get direct: %v", err)
	}

	// Invalidate 后该订阅的缓存被清除；再次 Get 同一 URL 会新建实例。
	f.Invalidate(1)
	t6, err := f.Get(1, "socks5://127.0.0.1:7898")
	if err != nil {
		t.Fatalf("Get #6 after invalidate: %v", err)
	}
	if t6 == t4 {
		t.Fatal("after Invalidate, Get must rebuild transport")
	}

	// CloseIdleConnections 不应 panic，且可继续 Get。
	f.CloseIdleConnections()
	if _, err := f.Get(9, "http://127.0.0.1:7897"); err != nil {
		t.Fatalf("Get after CloseIdleConnections: %v", err)
	}
}
