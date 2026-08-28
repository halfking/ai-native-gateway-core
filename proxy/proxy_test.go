package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProxyURLEncodesPortAsDigits 锁定一个曾经的真实 bug：端口用 string(rune(port))
// 拼接，8443 会变成一个 CJK 字符，生成的代理 URL 完全不可用。
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
	nodes []*Node
}

func (f *fakeStore) ListNodes(_ context.Context, subscriptionID *int) ([]*Node, error) {
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
